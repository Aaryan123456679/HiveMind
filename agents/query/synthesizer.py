"""Answer synthesis: prompt assembly + citation-annotated answer generation.

Per issue #24 subtask 4.5.1 and `docs/LLD/query-agent.md`'s "`synthesizer.py`" section
("Final LLM call: refined intent + concatenated selected markdown (with file-path headers)
-> answer with inline file-path citations."): given the refined-intent fields produced by
`intent_refiner.refine_intent` and the already-concatenated, file-path-headered markdown
selected by `topic_selector` (concatenation itself happens upstream -- out of scope for this
module, see "Input shape" below), build a synthesis prompt, call it through an `LLMClient`
(the same provider-agnostic DI interface used by `intent_refiner.py`,
`agents/llm/client.py`), and parse the model's completion into a validated
`SynthesizerResult` carrying the answer text plus the flat list of file paths it cited.

Subtask 4.5.2 ("Citation-format validation test") on the same issue builds dedicated
rejection/flagging logic and its own test file on top of this result -- this module only
needs to make the extracted citation list *complete and correct* and expose a defensible
building block (`SynthesizerResult.unknown_citations()`) for 4.5.2 to validate against; it
deliberately does not itself raise/reject on a hallucinated citation.

Input shape -- disclosed choice
--------------------------------
4.5.1's own "impacted modules" list names only `synthesizer.py`/`test_synthesizer.py` (no
`topic_selector.py` change), and its test spec takes "concatenated selected markdown with
file-path headers" as an already-assembled string. `topic_selector.py`'s own 4.4.3 section
explicitly leaves "mapping file_ids back to file content for synthesizer prompt" to a later,
not-yet-built subtask. This module therefore accepts `selected_markdown: str` directly (the
already-concatenated block) rather than a `Sequence[TopicCandidate]` or `list[int]` of
`file_id`s -- consistent with both the LLD's own wording and this subtask's scope.

Similarly, the refined-intent input is accepted as plain decoupled scalars
(`refined_intent`, `query_type`, `entities`) rather than a wrapped `IntentRefinerResult`,
mirroring `intent_refiner.refine_intent(query, history, ...)`'s own precedent of plain
scalar parameters over a request object.

File-path header format -- disclosed choice
----------------------------------------------
The LLD names "file-path headers" but does not specify their exact syntax. This module
adopts `## File: <path>` (a literal, level-2 markdown heading with the label `File:`) as
the expected per-section header in `selected_markdown` -- unambiguous to extract via regex,
human-readable, and ordinary markdown heading syntax rather than an invented delimiter.
`_extract_provided_paths()` scans `selected_markdown` for lines matching this format to
determine the "actually provided" file-path set; if a future upstream caller emits headers
in this format, they round-trip correctly. `selected_markdown` is embedded verbatim in the
prompt (headers included, unmodified), directly satisfying the test spec's "assert prompt
includes file-path headers".

LLM response wire format -- disclosed choice
------------------------------------------------
Per this run's `requirement.md`: this module mirrors `intent_refiner.py`'s
prompt-then-parse-JSON pattern rather than inventing a raw-markdown-with-regex-scraped-
citations scheme. The LLM is asked to return a single JSON object with:

```
{
  "answer": str,       # prose containing inline "[<path>]" citation markers
  "citations": [str]   # flat, deduplicated list of every cited file path
}
```

`answer`'s prose satisfies the acceptance criteria's "answer whose inline citations
reference actual file paths" (the citations appear inline, as `[<path>]` markers, per the
prompt's own instruction to the model); `citations` is the same information as a distinct,
directly machine-parseable field -- avoiding the need to re-derive the citation list by
regex-scraping `answer`'s free-form prose, and giving 4.5.2 a stable field to validate
against.

Prompt-then-parse-JSON pattern / code-fence stripping / exception design
----------------------------------------------------------------------------
Mirrors `intent_refiner.py` exactly: `strip_code_fences` (shared, top-level
`json_fences` module -- issue #55 subtask 4.5.17.2 relocated it there from the
private `ingestion._json_fences` so cross-package callers like this one and
`intent_refiner.py` don't reach into another package's private internals) before
`json.loads`; `SynthesizerError` is a *new* base
exception (NOT a subclass of `llm.client.LLMError` -- `LLMError` means the provider call
itself failed; this module's exceptions mean the call succeeded but its output could not be
turned into a valid result); `SynthesizerParseError` covers every malformed-output case with
a specific, descriptive message identifying which check failed.
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from typing import TYPE_CHECKING, Sequence

from json_fences import strip_code_fences

if TYPE_CHECKING:
    from llm.client import LLMClient

#: Regex matching a `## File: <path>` header line in `selected_markdown`. See module
#: docstring's "File-path header format" disclosure. `path` is the remainder of the line
#: after the `File:` label, stripped of surrounding whitespace.
_FILE_HEADER_RE = re.compile(r"^##\s*File:\s*(?P<path>.+?)\s*$", re.MULTILINE)

#: The complete set of required top-level keys in the LLM's JSON response.
_REQUIRED_STRING_FIELDS = ("answer",)
_REQUIRED_LIST_FIELDS = ("citations",)
_REQUIRED_FIELDS = _REQUIRED_STRING_FIELDS + _REQUIRED_LIST_FIELDS

_SYNTHESIS_PROMPT_TEMPLATE = """You are an answer-synthesis assistant for a knowledge-base \
search system. Given the user's refined intent and a set of selected source excerpts (each \
preceded by a "## File: <path>" header identifying the file it came from), write a clear, \
self-contained answer to the user's intent, citing your sources inline.

Refined intent: {refined_intent}
Query type: {query_type}
Relevant entities: {entities_block}

Selected source excerpts:
---
{selected_markdown}
---

Respond with ONLY a single JSON object (no prose, no markdown code fences) with exactly
these keys:
- "answer": a clear, self-contained answer to the refined intent, written in prose. Every
  factual claim must be followed by an inline citation in the exact form "[<path>]", where
  <path> is copied verbatim from one of the "## File: <path>" headers above. Never cite a
  path that is not one of the headers above.
- "citations": a JSON array of every file path string cited inline in "answer" (each
  path copied verbatim from a "## File: <path>" header above), deduplicated, in the order
  first cited.
"""


class SynthesizerError(Exception):
    """Base exception for this module's own answer-synthesis failures.

    Deliberately NOT a subclass of `llm.client.LLMError`: `LLMError` means the provider call
    itself failed; this module's exceptions mean the call succeeded but its output could not
    be turned into a valid result. See the module docstring's "Prompt-then-parse-JSON
    pattern" section.
    """


class SynthesizerParseError(SynthesizerError):
    """Raised when the LLM's completion string is not a valid synthesis result.

    Covers every malformed-output case: unparseable JSON, a missing required field, or a
    field of the wrong type. The exception message always identifies which specific check
    failed.
    """


@dataclass(frozen=True)
class SynthesizerResult:
    """A single validated answer-synthesis result, per `docs/LLD/query-agent.md`'s
    "`synthesizer.py`" output shape.

    Attributes:
        answer: The synthesized answer prose, containing inline "[<path>]" citation
            markers (see module docstring's "LLM response wire format" disclosure).
        citations: The flat, deduplicated list of file paths cited in `answer`, in the
            order first cited, as reported by the LLM's own `citations` field.
        provided_paths: The file paths actually present in the `selected_markdown` input
            that produced this result (extracted from its "## File: <path>" headers, in
            the order they first appeared), in dedup'd order. Not necessarily equal to
            `citations` -- see `unknown_citations()`.
    """

    answer: str
    citations: list[str]
    provided_paths: list[str]

    def unknown_citations(self) -> list[str]:
        """Return the subset of `citations` NOT present in `provided_paths`.

        A defensible building block for subtask 4.5.2's dedicated citation-format
        validation (hallucinated-citation detection/rejection): this method only reports
        which cited paths are unknown; it does not itself raise or reject. Order-preserved,
        deduplicated.
        """
        provided = set(self.provided_paths)
        seen: set[str] = set()
        unknown: list[str] = []
        for path in self.citations:
            if path not in provided and path not in seen:
                seen.add(path)
                unknown.append(path)
        return unknown


def _extract_provided_paths(selected_markdown: str) -> list[str]:
    """Extract the file paths named by `## File: <path>` headers in `selected_markdown`.

    Order-preserved, deduplicated (first occurrence wins). See module docstring's
    "File-path header format" disclosure for the exact header syntax expected.
    """
    seen: set[str] = set()
    paths: list[str] = []
    for match in _FILE_HEADER_RE.finditer(selected_markdown):
        path = match.group("path")
        if path not in seen:
            seen.add(path)
            paths.append(path)
    return paths


def _build_prompt(
    refined_intent: str,
    query_type: str,
    entities: Sequence[str],
    selected_markdown: str,
) -> str:
    """Render the synthesis prompt embedding all inputs, `selected_markdown` verbatim
    (including its "## File: <path>" headers, unmodified -- see module docstring)."""
    entities_block = ", ".join(entities) if entities else "(none)"
    return _SYNTHESIS_PROMPT_TEMPLATE.format(
        refined_intent=refined_intent,
        query_type=query_type,
        entities_block=entities_block,
        selected_markdown=selected_markdown,
    )


def _require_type(payload: dict, field: str, expected_type: type, type_name: str) -> object:
    value = payload[field]
    if not isinstance(value, expected_type):
        raise SynthesizerParseError(
            f"synthesizer: field {field!r} must be a {type_name}, got "
            f"{type(value).__name__}: {value!r}"
        )
    return value


def _parse_synthesis_json(raw: str, provided_paths: list[str]) -> SynthesizerResult:
    """Parse and validate `raw` (the LLM's completion string) into a `SynthesizerResult`.

    Args:
        raw: The LLM's raw completion string.
        provided_paths: The file paths extracted from the request's `selected_markdown`
            (via `_extract_provided_paths`), carried into the result unchanged.

    Raises:
        SynthesizerParseError: On any parse or validation failure. See the class docstring
            for the exact failure cases covered.
    """
    stripped = strip_code_fences(raw)
    try:
        payload = json.loads(stripped)
    except json.JSONDecodeError as exc:
        raise SynthesizerParseError(
            f"synthesizer: LLM response is not valid JSON: {exc}"
        ) from exc

    if not isinstance(payload, dict):
        raise SynthesizerParseError(
            f"synthesizer: LLM response must be a JSON object, got "
            f"{type(payload).__name__}"
        )

    missing = [field for field in _REQUIRED_FIELDS if field not in payload]
    if missing:
        raise SynthesizerParseError(
            f"synthesizer: LLM response missing required field(s): {', '.join(missing)}"
        )

    for field in _REQUIRED_STRING_FIELDS:
        _require_type(payload, field, str, "string")
    for field in _REQUIRED_LIST_FIELDS:
        value = _require_type(payload, field, list, "list")
        for i, item in enumerate(value):
            if not isinstance(item, str):
                raise SynthesizerParseError(
                    f"synthesizer: field {field!r}[{i}] must be a string, got "
                    f"{type(item).__name__}: {item!r}"
                )

    return SynthesizerResult(
        answer=payload["answer"],
        citations=list(payload["citations"]),
        provided_paths=provided_paths,
    )


def synthesize_answer(
    refined_intent: str,
    query_type: str,
    entities: Sequence[str],
    selected_markdown: str,
    llm_client: "LLMClient",
    *,
    model: str | None = None,
    temperature: float = 0.0,
    max_tokens: int | None = None,
    timeout: float | None = None,
) -> SynthesizerResult:
    """Synthesize a citation-annotated answer by calling `llm_client` and parsing its
    output.

    Args:
        refined_intent: The refined intent text (e.g. `IntentRefinerResult.refined_intent`).
        query_type: The refined query type (e.g. `IntentRefinerResult.query_type`).
        entities: Relevant entity name strings (e.g. `IntentRefinerResult.entities`).
        selected_markdown: The already-concatenated selected markdown, with each section
            preceded by a "## File: <path>" header (see module docstring's "File-path
            header format" disclosure). Embedded verbatim in the prompt.
        llm_client: The `LLMClient` used to perform the completion call.
        model, temperature, max_tokens, timeout: Forwarded verbatim to
            `llm_client.complete()`.

    Returns:
        A validated `SynthesizerResult`.

    Raises:
        LLMError: Propagated unwrapped if `llm_client.complete()` itself fails (provider
            call failure) -- not converted into `SynthesizerParseError`, since that means
            something different (see module docstring).
        SynthesizerParseError: If the LLM's completion string is not a valid synthesis
            result (unparseable JSON, missing/mistyped field).
    """
    provided_paths = _extract_provided_paths(selected_markdown)
    prompt = _build_prompt(refined_intent, query_type, entities, selected_markdown)
    raw = llm_client.complete(
        prompt,
        model=model,
        temperature=temperature,
        max_tokens=max_tokens,
        timeout=timeout,
    )
    return _parse_synthesis_json(raw, provided_paths)
