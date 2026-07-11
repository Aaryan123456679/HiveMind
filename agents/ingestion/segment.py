"""Segmentation: LLM call + structured JSON output parsing/validation.

Per issue #18 subtask 3.4.3 and `docs/LLD/ingestion-agent.md`'s "Segmentation
agent" section: given a document's text (as a :class:`~ingestion.rawdoc.RawDocument`)
and a bounded topic shortlist (as produced by `ingestion.shortlist.shortlist()`,
3.4.2), build a segmentation prompt, call it through an `LLMClient` (3.4.1),
and parse the model's raw completion string into a validated
:class:`SegmentResult` matching the LLD's flat JSON shape:

```
{
  topic_action: APPEND_EXISTING | CREATE_NEW,
  target_topic,
  new_topic_path,
  content_markdown,
  entities: [],
  related_topics: []
}
```

`RawDocument` vs. a bare string -- disclosed choice
------------------------------------------------------
`segment()` takes a `ingestion.rawdoc.RawDocument`, not a bare `str`, even
though only its `.text` field is actually used by the prompt today.
`RawDocument`'s own docstring calls `.text` "the primary content downstream
segmentation operates on", i.e. it already documents itself as this module's
intended input; accepting the full record (rather than requiring every
caller to unpack `.text` first) keeps this module's call sites consistent
with the rest of the `agents/ingestion/` pipeline and leaves room for a
future prompt revision to use `.structured_fields`/`.source_type` without a
signature change.

`entities` / `related_topics` element shape -- disclosed choice
--------------------------------------------------------------------
The issue body and `docs/LLD/ingestion-agent.md` both show these as bare
JSON arrays (`entities: []`, `related_topics: []`) with no richer element
shape specified anywhere -- including at the RPC layer: `PutSegmentRequest`
in `proto/hivemind.proto` carries only `file_id` + raw `content` bytes, no
entity/related-topic schema. This module therefore treats both as
`list[str]` (plain entity names / topic-path strings), matching the LLD's
literal shape and `ingestion.shortlist.TopicCandidate.path`'s own
plain-string convention.

Exception design -- disclosed choice
----------------------------------------
`SegmentError` is a *new* base exception (not a subclass of
`llm.client.LLMError`): `LLMError` means "the provider call itself failed";
the failures this module raises are the opposite case -- the provider call
*succeeded* and returned a string, but that string is not a valid segment.
Conflating the two under one exception type would make it impossible for a
caller to distinguish "retry the LLM call" from "the model's output was
unusable, stop retrying with the same prompt." A single concrete subclass,
`SegmentParseError`, covers every malformed-output case (unparseable JSON,
missing/mistyped field, internally-inconsistent field combination) with a
specific, descriptive message identifying which check failed -- this issue's
acceptance criteria asks for "a clear error", not a taxonomy of exception
subclasses per failure reason, and the existing `agents/llm/` convention
(`OllamaClientError` used for every HTTP/parse failure kind) mirrors this.

Cross-field validation strictness -- disclosed choice
----------------------------------------------------------
`topic_action == "APPEND_EXISTING"` requires `target_topic` to be a
non-empty string, and `"CREATE_NEW"` requires `new_topic_path` to be a
non-empty string -- this is the one cross-field rule the issue's own example
malformed case describes ("APPEND_EXISTING with no target_topic"). This
module deliberately does NOT additionally require `target_topic` to be one
of the shortlist's own paths: `ingestion.shortlist.shortlist()`'s own
docstring describes the shortlist as a bounded, re-ranked *subset* of the
catalog (BM25 top-k over a bounded pool), not an exhaustive membership list,
so a legitimate existing topic the shortlist happened not to surface would
be incorrectly rejected by a strict membership check. Validation here stays
about the JSON's own *structural* correctness, not a second-guessing of the
model's topic-selection judgment.

Markdown-code-fence tolerance -- F1 closed (subtask 3.4.6)
------------------------------------------------------------
Previously (3.4.3), this module called `json.loads` on the raw completion string
unconditionally, so a real Ollama-backed model that ignored the prompt's "no markdown
code fences" instruction and wrapped its JSON in ` ```json ... ``` ` would be rejected
outright by `SegmentParseError("... is not valid JSON ...")`, even though the enclosed
payload was perfectly well-formed. This was flagged forward as finding F1
(`.cdr/index/regression.jsonl`), non-blocking at the time because 3.4.3's own tests
were mocked and never exercised a real model's response shape.
`ingestion.propose_split` (3.4.5) already had to solve the exact same problem and
carried its own private fence-stripping helper. Subtask 3.4.6 closes F1 by extracting
that helper into the shared `ingestion._json_fences.strip_code_fences` (both modules
now import it, rather than segment.py re-deriving the same regex independently or
propose_split.py's copy silently drifting out of sync), and calling it here before
`json.loads`, mirroring `propose_split.py`'s existing, already-proven pattern.

Control-character / triple-quote tolerance -- F7 closed (issue #44)
--------------------------------------------------------------------
A *distinct* real-model reliability gap from F1 was surfaced by issue #19 subtask
3.5.2's real end-to-end smoke run against a live local `llama3.1:8b` model:
~7/11 real documents produced completions that still failed `json.loads` even after
`strip_code_fences` ran cleanly, because the model embedded raw control characters
or a stray `\"\"\"` triple-quote artifact directly inside a JSON string value (see
`ingestion._json_fences`'s module docstring for the exact failure shapes/error
messages). That subtask deliberately left `_parse_segment_json` unmodified (out of
scope); issue #44 closes F7 by retrying once, only after the first `json.loads`
attempt has already failed, through the shared
`ingestion._json_fences.sanitize_control_chars_and_triple_quotes` helper before
raising `SegmentParseError`. Gating the retry behind the first failure (rather than
always sanitizing) means the already-working happy path is untouched -- it can only
turn some previously-failing malformed completions into successfully-parsed ones,
never change behavior for already-valid JSON.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import TYPE_CHECKING, Literal, Sequence

from json_fences import sanitize_control_chars_and_triple_quotes, strip_code_fences

if TYPE_CHECKING:
    from llm.client import LLMClient
    from ingestion.rawdoc import RawDocument
    from ingestion.shortlist import TopicCandidate

#: The two `topic_action` values the LLD/issue define. See module docstring.
SegmentAction = Literal["APPEND_EXISTING", "CREATE_NEW"]

_VALID_ACTIONS: frozenset[str] = frozenset({"APPEND_EXISTING", "CREATE_NEW"})

#: The complete set of required top-level keys in the LLM's JSON response.
_REQUIRED_STRING_FIELDS = ("topic_action", "target_topic", "new_topic_path", "content_markdown")
_REQUIRED_LIST_FIELDS = ("entities", "related_topics")
_REQUIRED_FIELDS = _REQUIRED_STRING_FIELDS + _REQUIRED_LIST_FIELDS

_SEGMENT_PROMPT_TEMPLATE = """You are a document segmentation assistant. Decide how the
document below should be filed into a topic knowledge base.

You are given a shortlist of existing candidate topics (paths). Decide whether this
document's content belongs appended to one of these existing topics, or whether it
warrants a new topic.

Existing candidate topics:
{shortlist_block}

Document text:
---
{document_text}
---

Respond with ONLY a single JSON object (no prose, no markdown code fences) with
exactly these keys:
- "topic_action": either the literal string "APPEND_EXISTING" or "CREATE_NEW".
- "target_topic": if topic_action is "APPEND_EXISTING", the exact path of the existing
  topic to append to (from the shortlist above); otherwise "".
- "new_topic_path": if topic_action is "CREATE_NEW", the path for the new topic;
  otherwise "".
- "content_markdown": the markdown-formatted content to file under that topic.
- "entities": a JSON array of entity name strings mentioned in the document.
- "related_topics": a JSON array of topic path strings related to this content.
"""


class SegmentError(Exception):
    """Base exception for this module's own segmentation failures.

    Deliberately NOT a subclass of `llm.client.LLMError`: `LLMError` means the
    provider call itself failed; this module's exceptions mean the call
    succeeded but its output could not be turned into a valid segment. See
    the module docstring's "Exception design" section.
    """


class SegmentParseError(SegmentError):
    """Raised when the LLM's completion string is not a valid segment.

    Covers every malformed-output case: unparseable JSON, a missing required
    field, a field of the wrong type, an out-of-range enum value, or an
    internally inconsistent field combination (e.g. `APPEND_EXISTING` with no
    `target_topic`). The exception message always identifies which specific
    check failed.
    """


@dataclass(frozen=True)
class SegmentResult:
    """A single validated segment, per `docs/LLD/ingestion-agent.md`'s
    "Segmentation agent" output shape.

    Attributes:
        topic_action: `"APPEND_EXISTING"` or `"CREATE_NEW"`.
        target_topic: The existing topic's path to append to. Populated
            (non-empty) iff `topic_action == "APPEND_EXISTING"`; otherwise
            `""`.
        new_topic_path: The path for a newly created topic. Populated
            (non-empty) iff `topic_action == "CREATE_NEW"`; otherwise `""`.
        content_markdown: The markdown content to file under the resolved
            topic.
        entities: Entity name strings mentioned in the document. See module
            docstring's "element shape" disclosure for why this is
            `list[str]`, not a richer per-entity record.
        related_topics: Related topic path strings (become `LLM_ASSERTED`
            graph edges downstream, per the LLD -- not this module's
            concern).
    """

    topic_action: SegmentAction
    target_topic: str
    new_topic_path: str
    content_markdown: str
    entities: list[str]
    related_topics: list[str]


def _build_prompt(doc: "RawDocument", shortlist: Sequence["TopicCandidate"]) -> str:
    """Render the segmentation prompt embedding `doc.text` and `shortlist`'s paths."""
    if shortlist:
        shortlist_block = "\n".join(f"- {candidate.path}" for candidate in shortlist)
    else:
        shortlist_block = "(no existing candidate topics)"
    return _SEGMENT_PROMPT_TEMPLATE.format(
        shortlist_block=shortlist_block,
        document_text=doc.text,
    )


def _require_type(payload: dict, field: str, expected_type: type, type_name: str) -> object:
    value = payload[field]
    if not isinstance(value, expected_type):
        raise SegmentParseError(
            f"segment: field {field!r} must be a {type_name}, got "
            f"{type(value).__name__}: {value!r}"
        )
    return value


def _parse_segment_json(raw: str) -> SegmentResult:
    """Parse and validate `raw` (the LLM's completion string) into a `SegmentResult`.

    Raises:
        SegmentParseError: On any parse or validation failure. See the class
            docstring for the exact failure cases covered.
    """
    stripped = strip_code_fences(raw)
    try:
        payload = json.loads(stripped)
    except json.JSONDecodeError:
        # Fallback only -- see module docstring's "Control-character / triple-quote
        # tolerance -- F7 closed (issue #44)" section. Never runs on the
        # already-working happy path (first json.loads already succeeded above);
        # only attempted once the raw completion has already failed to parse.
        sanitized = sanitize_control_chars_and_triple_quotes(stripped)
        try:
            payload = json.loads(sanitized)
        except json.JSONDecodeError as exc:
            raise SegmentParseError(
                f"segment: LLM response is not valid JSON: {exc}"
            ) from exc

    if not isinstance(payload, dict):
        raise SegmentParseError(
            f"segment: LLM response must be a JSON object, got "
            f"{type(payload).__name__}"
        )

    missing = [field for field in _REQUIRED_FIELDS if field not in payload]
    if missing:
        raise SegmentParseError(
            f"segment: LLM response missing required field(s): {', '.join(missing)}"
        )

    for field in _REQUIRED_STRING_FIELDS:
        _require_type(payload, field, str, "string")
    for field in _REQUIRED_LIST_FIELDS:
        value = _require_type(payload, field, list, "list")
        for i, item in enumerate(value):
            if not isinstance(item, str):
                raise SegmentParseError(
                    f"segment: field {field!r}[{i}] must be a string, got "
                    f"{type(item).__name__}: {item!r}"
                )

    topic_action = payload["topic_action"]
    if topic_action not in _VALID_ACTIONS:
        raise SegmentParseError(
            f"segment: field 'topic_action' must be one of "
            f"{sorted(_VALID_ACTIONS)}, got {topic_action!r}"
        )

    target_topic = payload["target_topic"]
    new_topic_path = payload["new_topic_path"]
    if topic_action == "APPEND_EXISTING" and not target_topic:
        raise SegmentParseError(
            "segment: topic_action is 'APPEND_EXISTING' but 'target_topic' is empty"
        )
    if topic_action == "CREATE_NEW" and not new_topic_path:
        raise SegmentParseError(
            "segment: topic_action is 'CREATE_NEW' but 'new_topic_path' is empty"
        )

    return SegmentResult(
        topic_action=topic_action,
        target_topic=target_topic,
        new_topic_path=new_topic_path,
        content_markdown=payload["content_markdown"],
        entities=list(payload["entities"]),
        related_topics=list(payload["related_topics"]),
    )


def segment(
    doc: "RawDocument",
    shortlist: Sequence["TopicCandidate"],
    llm_client: "LLMClient",
    *,
    model: str | None = None,
    temperature: float = 0.0,
    max_tokens: int | None = None,
    timeout: float | None = None,
) -> SegmentResult:
    """Segment `doc` against `shortlist` by calling `llm_client` and parsing its output.

    Args:
        doc: The document to segment. Only `doc.text` is used by the prompt
            (see module docstring for why the full `RawDocument` is accepted).
        shortlist: The bounded candidate topic list (e.g. from
            `ingestion.shortlist.shortlist()`) embedded into the prompt.
        llm_client: The `LLMClient` used to perform the completion call.
        model, temperature, max_tokens, timeout: Forwarded verbatim to
            `llm_client.complete()`.

    Returns:
        A validated `SegmentResult`.

    Raises:
        LLMError: Propagated unwrapped if `llm_client.complete()` itself
            fails (provider call failure) -- not converted into
            `SegmentParseError`, since that means something different (see
            module docstring).
        SegmentParseError: If the LLM's completion string is not a valid
            segment (unparseable JSON, missing/mistyped field, or an
            internally inconsistent field combination).
    """
    prompt = _build_prompt(doc, shortlist)
    raw = llm_client.complete(
        prompt,
        model=model,
        temperature=temperature,
        max_tokens=max_tokens,
        timeout=timeout,
    )
    return _parse_segment_json(raw)
