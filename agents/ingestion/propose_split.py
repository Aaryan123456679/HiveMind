"""`ProposeSplit`: LLM-backed, topic-coherent auto-split proposal.

Per issue #18 subtask 3.4.5 and `docs/LLD/ingestion-agent.md`'s "``ProposeSplit``"
section:

```
ProposeSplit(fileContent) -> [{newPath, sectionRanges}, ...] + redirect summary
```

Called by `engine/split/` (task-3.2.3's `GRPCSplitProposer`, already implemented) when
a file crosses its auto-split size threshold (`docs/LLD/split.md`'s "Trigger" section --
detecting the threshold crossing is entirely `engine/split/`'s responsibility, not
this module's). This module implements the *callee* side: given the full content of an
over-threshold file, decide a topic-coherent split into multiple new files and a
human-readable redirect summary, using the "segmentation LLM pathway" (an
`agents.llm.client.LLMClient`, per the issue text) -- the same provider abstraction
`agents/ingestion/segment.py` (3.4.3) uses.

Where this actually runs -- disclosed architecture note
---------------------------------------------------------
`proto/hivemind.proto`'s own top comment and `docs/LLD/rpc.md`'s "Status" note both
state that `ProposeSplit`'s real server-side implementation lives here, in
`agents/ingestion/`, as the gRPC *callee*, while `engine/rpc/server.go` intentionally
never implements it (the Go engine is the gRPC *client* for this one RPC, via
`engine/split/proposer_grpc.go`, task-3.2.3). This module supplies the business logic
that a future `agents.hivemind_pb2_grpc.HiveMindServicer.ProposeSplit` implementation
would delegate to; standing up an actual running `grpc.Server` process in `agents/` is
new scope beyond this subtask's named impacted modules
(`agents/ingestion/propose_split.py`, `agents/ingestion/test_propose_split.py`) and is
not done here -- see this run's `handoff.json`.

Deterministic partition guarantee -- disclosed design
--------------------------------------------------------
The issue's own test spec requires the returned plan's `SectionRange`s to "partition
content without gaps or overlaps." LLMs cannot reliably compute exact byte offsets, so
this module does not trust any LLM-reported offset directly. Instead, the LLM is asked
only for each section's target topic path plus a `start_marker` -- a short, verbatim
substring of the document text marking where that section begins (the first section's
own marker is accepted but not load-bearing; see `_resolve_section_ranges`). This
module then *locates* each marker deterministically, monotonically forward from the
previously resolved boundary via `str.find`, and constructs `SectionRange`s by
construction: the first section always starts at byte `0`, the last always ends at
`len(file_content)`, and every interior boundary is exactly the resolved offset of the
next section's marker. This guarantees the partition invariant holds by construction
regardless of what the LLM said, and turns any unresolvable/out-of-order marker into an
explicit `ProposeSplitParseError` rather than a silently wrong offset -- the same
"code enforces structural correctness, the LLM only supplies judgment" split of
responsibility `segment.py` already uses for its own JSON shape.

One `SectionRange` per file -- disclosed simplification
-------------------------------------------------------------
`proto/hivemind.proto`'s `SplitFileProposal.section_ranges` is `repeated`, technically
allowing one output file to be assembled from multiple non-contiguous ranges of the
original content. This module always produces exactly one (contiguous) `SectionRange`
per proposed file -- the simplest reading consistent with "topic-coherent split" and
sufficient to satisfy the partition invariant trivially (each file's single range is
itself one contiguous slice of the overall partition). Scattered/interleaved
multi-range files are not attempted; flagged forward as a disclosed scope reduction,
not a defect.

Markdown-code-fence defensiveness -- shared helper, F1 now closed
------------------------------------------------------------------------------------
Real Ollama-backed models sometimes wrap JSON completions in markdown code fences
despite being told not to. This module's parser proactively strips a single
leading/trailing ``` ```(json)?...``` ``` fence (if present) before `json.loads`, via
the shared `json_fences.strip_code_fences` helper.

Historical note: `agents/ingestion/segment.py` (3.4.3) originally lacked this
guard -- an open, non-blocking finding (F1, `.cdr/index/regression.jsonl`) explicitly
forwarded from this module's own defensiveness to `segment.py`'s missing equivalent.
Subtask 3.4.6 closed F1 by extracting this module's original private
`_strip_code_fences`/`_CODE_FENCE_RE` into a shared module and wiring `segment.py` to
use it too, rather than leaving `segment.py`'s gap open or letting a second
independent copy of the same regex drift out of sync. Subtask 4.5.17.2 (issue #55)
later relocated that shared module from the private `ingestion._json_fences` to the
top-level, public `json_fences` module so `query.intent_refiner` could import it
without reaching into another package's private internals. This module's own
behavior is unchanged by either extraction.

Exception design
-----------------
Mirrors `segment.py`: `ProposeSplitError` is the base (deliberately not a subclass of
`llm.client.LLMError` -- provider-call failure and unusable-output-after-a-successful-
call are different failure classes a caller needs to distinguish).
`ProposeSplitParseError` covers every malformed-output/unresolvable-marker case with a
message identifying which check failed.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Sequence

from json_fences import strip_code_fences

if TYPE_CHECKING:
    from llm.client import LLMClient

#: Minimum number of sections a real split proposal must contain: a "split" that
#: produces fewer than 2 output files is not a split at all.
_MIN_SECTIONS = 2

_REQUIRED_SECTION_STRING_FIELDS = ("new_topic_path", "start_marker")

_SPLIT_PROMPT_TEMPLATE = """You are a document-splitting assistant. The document below \
has grown too large for a single topic file and must be split into multiple \
topic-coherent files.

Decide how to split the document into 2 or more contiguous, topic-coherent sections, \
in the order they appear in the document. For each section, choose a new topic path \
and a short verbatim "start marker" -- an exact substring (a few words, copied \
character-for-character from the document) that marks where that section begins in \
the document text below. Markers must appear in the same order as the sections, \
each occurring later in the document than the previous one.

Document text:
---
{document_text}
---

Respond with ONLY a single JSON object (no prose, no markdown code fences) with \
exactly these keys:
- "sections": a JSON array, in document order, of objects each with:
  - "new_topic_path": the path for this section's new topic file (non-empty string).
  - "start_marker": an exact verbatim substring of the document text marking this \
section's start.
- "redirect_summary": a short human-readable string summarizing the split, to be \
recorded at the original file's redirect stub.
"""


class ProposeSplitError(Exception):
    """Base exception for this module's own split-proposal failures.

    Deliberately NOT a subclass of `llm.client.LLMError`; see module docstring's
    "Exception design" section.
    """


class ProposeSplitParseError(ProposeSplitError):
    """Raised when the LLM's completion string is not a valid split proposal.

    Covers unparseable JSON, a missing/mistyped field, fewer than
    `_MIN_SECTIONS` sections, a duplicate `new_topic_path`, or a `start_marker`
    that cannot be located (in order) within the original document content. The
    exception message always identifies which specific check failed.
    """


@dataclass(frozen=True)
class SectionRange:
    """A half-open byte-offset range `[start, end)` into the original file content.

    Mirrors `proto/hivemind.proto`'s `SectionRange` message field-for-field.
    """

    start: int
    end: int


@dataclass(frozen=True)
class SplitFileProposal:
    """One proposed output file: a new topic path plus the byte ranges of the
    original content it is built from.

    Mirrors `proto/hivemind.proto`'s `SplitFileProposal` message. This module always
    populates exactly one (contiguous) `SectionRange` per proposal -- see module
    docstring's "One SectionRange per file" disclosure.
    """

    new_path: str
    section_ranges: list[SectionRange] = field(default_factory=list)


@dataclass(frozen=True)
class ProposeSplitResult:
    """The full split proposal: an ordered list of output files plus a redirect
    summary, mirroring `proto/hivemind.proto`'s `ProposeSplitResponse`.
    """

    files: list[SplitFileProposal]
    redirect_summary: str


def _build_prompt(document_text: str) -> str:
    return _SPLIT_PROMPT_TEMPLATE.format(document_text=document_text)


def _require_type(payload: dict, field_name: str, expected_type: type, type_name: str) -> object:
    value = payload[field_name]
    if not isinstance(value, expected_type):
        raise ProposeSplitParseError(
            f"propose_split: field {field_name!r} must be a {type_name}, got "
            f"{type(value).__name__}: {value!r}"
        )
    return value


def _parse_propose_split_json(raw: str) -> tuple[list[dict], str]:
    """Parse and structurally validate `raw` (the LLM's completion string).

    Returns:
        `(sections, redirect_summary)`, where `sections` is the raw list of
        per-section dicts (not yet resolved to byte offsets) and `redirect_summary`
        is the validated summary string.

    Raises:
        ProposeSplitParseError: On any parse or structural-validation failure.
    """
    stripped = strip_code_fences(raw)
    try:
        payload = json.loads(stripped)
    except json.JSONDecodeError as exc:
        raise ProposeSplitParseError(
            f"propose_split: LLM response is not valid JSON: {exc}"
        ) from exc

    if not isinstance(payload, dict):
        raise ProposeSplitParseError(
            f"propose_split: LLM response must be a JSON object, got "
            f"{type(payload).__name__}"
        )

    missing = [f for f in ("sections", "redirect_summary") if f not in payload]
    if missing:
        raise ProposeSplitParseError(
            f"propose_split: LLM response missing required field(s): {', '.join(missing)}"
        )

    redirect_summary = _require_type(payload, "redirect_summary", str, "string")

    sections = _require_type(payload, "sections", list, "list")
    if len(sections) < _MIN_SECTIONS:
        raise ProposeSplitParseError(
            f"propose_split: field 'sections' must contain at least {_MIN_SECTIONS} "
            f"entries (a split into fewer than {_MIN_SECTIONS} files is not a split), "
            f"got {len(sections)}"
        )

    seen_paths: set[str] = set()
    for i, section in enumerate(sections):
        if not isinstance(section, dict):
            raise ProposeSplitParseError(
                f"propose_split: field 'sections'[{i}] must be a JSON object, got "
                f"{type(section).__name__}"
            )
        section_missing = [
            f for f in _REQUIRED_SECTION_STRING_FIELDS if f not in section
        ]
        if section_missing:
            raise ProposeSplitParseError(
                f"propose_split: field 'sections'[{i}] missing required field(s): "
                f"{', '.join(section_missing)}"
            )
        for f in _REQUIRED_SECTION_STRING_FIELDS:
            value = section[f]
            if not isinstance(value, str):
                raise ProposeSplitParseError(
                    f"propose_split: field 'sections'[{i}].{f} must be a string, "
                    f"got {type(value).__name__}: {value!r}"
                )
        new_topic_path = section["new_topic_path"]
        if not new_topic_path:
            raise ProposeSplitParseError(
                f"propose_split: field 'sections'[{i}].new_topic_path must be "
                f"non-empty"
            )
        if not section["start_marker"]:
            raise ProposeSplitParseError(
                f"propose_split: field 'sections'[{i}].start_marker must be non-empty"
            )
        if new_topic_path in seen_paths:
            raise ProposeSplitParseError(
                f"propose_split: duplicate new_topic_path {new_topic_path!r} across "
                f"multiple sections; each proposed file must have a distinct path"
            )
        seen_paths.add(new_topic_path)

    return sections, redirect_summary


def _resolve_section_ranges(
    document_text: str, sections: Sequence[dict]
) -> list[SectionRange]:
    """Resolve `sections`' `start_marker`s into a partition of `document_text`
    (as character offsets over `document_text`; callers convert to byte offsets).

    The first section's own range always starts at character `0` (covering any
    preamble before its marker, which is not itself load-bearing for offset
    purposes); the last section's range always ends at `len(document_text)`. Every
    interior boundary is the resolved character offset of the *next* section's
    `start_marker`, located via a monotonic forward `str.find` from the previously
    resolved boundary -- guaranteeing the returned ranges are sorted, contiguous, and
    gap/overlap-free by construction.

    Raises:
        ProposeSplitParseError: If any section (other than the first) has a
            `start_marker` that cannot be found at or after the previous boundary,
            or that would resolve to a zero-length section.
    """
    n = len(sections)
    boundaries = [0]
    search_from = 0
    for i in range(1, n):
        marker = sections[i]["start_marker"]
        idx = document_text.find(marker, search_from)
        if idx == -1:
            raise ProposeSplitParseError(
                f"propose_split: sections[{i}].start_marker {marker!r} was not "
                f"found in the document at or after character offset {search_from} "
                f"(markers must appear in document order, each after the previous "
                f"section's boundary)"
            )
        if idx <= boundaries[-1]:
            raise ProposeSplitParseError(
                f"propose_split: sections[{i}].start_marker {marker!r} resolves to "
                f"offset {idx}, which is not strictly after the previous section's "
                f"boundary at offset {boundaries[-1]} (would produce a zero-length "
                f"or out-of-order section)"
            )
        boundaries.append(idx)
        search_from = idx

    boundaries.append(len(document_text))

    return [
        SectionRange(start=boundaries[i], end=boundaries[i + 1]) for i in range(n)
    ]


def _char_offset_to_byte_offset(document_text: str, char_offset: int) -> int:
    """Convert a character offset into `document_text` to a byte offset in its
    UTF-8 encoding, matching `proto/hivemind.proto`'s `SectionRange` byte-offset
    contract.
    """
    return len(document_text[:char_offset].encode("utf-8"))


def propose_split(
    file_content: bytes,
    llm_client: "LLMClient",
    *,
    model: str | None = None,
    temperature: float = 0.0,
    max_tokens: int | None = None,
    timeout: float | None = None,
) -> ProposeSplitResult:
    """Propose a topic-coherent split of `file_content` by calling `llm_client`.

    Args:
        file_content: The full content of the over-threshold file (matches
            `ProposeSplitRequest.file_content`'s `bytes` type). Detecting that the
            file is over-threshold is `engine/split/`'s responsibility, not this
            function's -- it always attempts a split when called.
        llm_client: The `LLMClient` used to perform the completion call (the
            "segmentation LLM pathway").
        model, temperature, max_tokens, timeout: Forwarded verbatim to
            `llm_client.complete()`.

    Returns:
        A `ProposeSplitResult` whose `files[*].section_ranges` -- taken together, in
        order -- partition `file_content` with no gaps or overlaps: the first range
        starts at byte `0`, the last ends at `len(file_content)`, and each
        subsequent range's start equals the previous range's end.

    Raises:
        LLMError: Propagated unwrapped if `llm_client.complete()` itself fails
            (provider call failure) -- not converted into `ProposeSplitParseError`,
            mirroring `segment.py`'s convention.
        ProposeSplitParseError: If the LLM's completion string is not a valid split
            proposal (unparseable JSON, missing/mistyped field, fewer than 2
            sections, a duplicate topic path, or an unresolvable/out-of-order
            `start_marker`).
        UnicodeDecodeError: If `file_content` is not valid UTF-8 (this module
            operates on decoded text for prompting/marker-resolution, per the LLD's
            markdown-file assumption throughout `agents/ingestion/`).
    """
    document_text = file_content.decode("utf-8")
    prompt = _build_prompt(document_text)
    raw = llm_client.complete(
        prompt,
        model=model,
        temperature=temperature,
        max_tokens=max_tokens,
        timeout=timeout,
    )
    sections, redirect_summary = _parse_propose_split_json(raw)
    char_ranges = _resolve_section_ranges(document_text, sections)

    files = [
        SplitFileProposal(
            new_path=section["new_topic_path"],
            section_ranges=[
                SectionRange(
                    start=_char_offset_to_byte_offset(document_text, char_range.start),
                    end=_char_offset_to_byte_offset(document_text, char_range.end),
                )
            ],
        )
        for section, char_range in zip(sections, char_ranges)
    ]

    return ProposeSplitResult(files=files, redirect_summary=redirect_summary)
