"""Tests for `ingestion.propose_split.propose_split` and its structured JSON output
parsing / deterministic marker-based range resolution.

Per issue #18 subtask 3.4.5's test spec: `LLMClient.complete()` is mocked (via a
small fake `LLMClient` subclass, matching `test_segment.py`'s own convention), with a
fixture over-threshold document. The core acceptance check is that the returned
plan's `SectionRange`s -- taken together across all proposed files, in order --
partition the original content with no gaps or overlaps.
"""

from __future__ import annotations

import json

import pytest

from ingestion.propose_split import (
    ProposeSplitParseError,
    ProposeSplitResult,
    SectionRange,
    propose_split,
)
from llm.client import LLMClient, LLMError

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


class _FakeLLMClient(LLMClient):
    """Minimal `LLMClient` stand-in returning a pre-configured canned string.

    Mirrors `test_segment.py`'s `_FakeLLMClient` exactly.
    """

    def __init__(self, response: str | None = None, error: Exception | None = None) -> None:
        self.response = response
        self.error = error
        self.calls: list[dict] = []

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        self.calls.append(
            {
                "prompt": prompt,
                "model": model,
                "temperature": temperature,
                "max_tokens": max_tokens,
                "timeout": timeout,
            }
        )
        if self.error is not None:
            raise self.error
        assert self.response is not None
        return self.response


#: A fixture "over-threshold" document: three clearly distinct topics concatenated,
#: well over any realistic single-topic size, matching the issue's "fixture
#: over-threshold document" test-spec wording.
_FIXTURE_DOCUMENT = (
    "# Billing overview\n\n"
    "This section covers invoice disputes and how customers report them. "
    "Invoice 4521 was flagged as a duplicate charge by the customer.\n\n"
    "# Shipping overview\n\n"
    "This section covers shipment tracking and delivery delays. "
    "Package 998 was delayed at the regional hub for three days.\n\n"
    "# Support overview\n\n"
    "This section covers general support ticket triage and escalation policy. "
    "Ticket 77 was escalated to tier 2 after no response for 48 hours.\n"
).encode("utf-8")

_VALID_PAYLOAD = {
    "sections": [
        {"new_topic_path": "billing/Overview", "start_marker": "# Billing overview"},
        {"new_topic_path": "shipping/Overview", "start_marker": "# Shipping overview"},
        {"new_topic_path": "support/Overview", "start_marker": "# Support overview"},
    ],
    "redirect_summary": "Split into billing, shipping, and support overview topics.",
}


def _decoded(document: bytes = _FIXTURE_DOCUMENT) -> str:
    return document.decode("utf-8")


# ---------------------------------------------------------------------------
# Well-formed responses -- the core partition-invariant acceptance check
# ---------------------------------------------------------------------------


def test_propose_split_partitions_content_without_gaps_or_overlaps() -> None:
    client = _FakeLLMClient(response=json.dumps(_VALID_PAYLOAD))
    result = propose_split(_FIXTURE_DOCUMENT, client)

    assert isinstance(result, ProposeSplitResult)
    assert len(result.files) == 3

    # Flatten every file's (single) range into one ordered list, per module
    # docstring's "one SectionRange per file" design.
    ranges: list[SectionRange] = []
    for proposal in result.files:
        assert len(proposal.section_ranges) == 1
        ranges.append(proposal.section_ranges[0])

    # Partition invariant: sorted, contiguous, no gaps, no overlaps, covers the
    # entire document exactly once.
    assert ranges[0].start == 0
    assert ranges[-1].end == len(_FIXTURE_DOCUMENT)
    for prev, nxt in zip(ranges, ranges[1:]):
        assert prev.end == nxt.start
    for r in ranges:
        assert r.start < r.end

    # Reassembling the ranges reproduces the original content exactly.
    reassembled = b"".join(_FIXTURE_DOCUMENT[r.start : r.end] for r in ranges)
    assert reassembled == _FIXTURE_DOCUMENT


def test_propose_split_preserves_topic_paths_and_order() -> None:
    client = _FakeLLMClient(response=json.dumps(_VALID_PAYLOAD))
    result = propose_split(_FIXTURE_DOCUMENT, client)

    assert [f.new_path for f in result.files] == [
        "billing/Overview",
        "shipping/Overview",
        "support/Overview",
    ]


def test_propose_split_returns_redirect_summary() -> None:
    client = _FakeLLMClient(response=json.dumps(_VALID_PAYLOAD))
    result = propose_split(_FIXTURE_DOCUMENT, client)

    assert result.redirect_summary == _VALID_PAYLOAD["redirect_summary"]


def test_prompt_includes_document_text() -> None:
    client = _FakeLLMClient(response=json.dumps(_VALID_PAYLOAD))
    propose_split(_FIXTURE_DOCUMENT, client)

    assert len(client.calls) == 1
    prompt = client.calls[0]["prompt"]
    assert "Billing overview" in prompt
    assert "Shipping overview" in prompt


def test_propose_split_forwards_call_kwargs() -> None:
    client = _FakeLLMClient(response=json.dumps(_VALID_PAYLOAD))
    propose_split(
        _FIXTURE_DOCUMENT,
        client,
        model="llama3.1:8b",
        temperature=0.1,
        max_tokens=1024,
        timeout=45.0,
    )

    call = client.calls[0]
    assert call["model"] == "llama3.1:8b"
    assert call["temperature"] == 0.1
    assert call["max_tokens"] == 1024
    assert call["timeout"] == 45.0


def test_llm_error_propagates_unwrapped() -> None:
    client = _FakeLLMClient(error=LLMError("provider call failed"))
    with pytest.raises(LLMError):
        propose_split(_FIXTURE_DOCUMENT, client)


def test_markdown_code_fence_wrapped_json_is_parsed() -> None:
    """Regression guard for the shared `ingestion._json_fences.strip_code_fences`
    helper (originally this module's own private code, extracted in 3.4.6 when
    `segment.py`'s equivalent gap was closed as F1, `.cdr/index/regression.jsonl`) --
    a fenced but otherwise well-formed JSON response must still parse successfully,
    not raise `ProposeSplitParseError`.
    """
    fenced = "```json\n" + json.dumps(_VALID_PAYLOAD) + "\n```"
    client = _FakeLLMClient(response=fenced)
    result = propose_split(_FIXTURE_DOCUMENT, client)
    assert len(result.files) == 3


# ---------------------------------------------------------------------------
# Malformed responses
# ---------------------------------------------------------------------------


def test_unparseable_json_raises() -> None:
    client = _FakeLLMClient(response="this is not JSON at all {{{")
    with pytest.raises(ProposeSplitParseError, match="not valid JSON"):
        propose_split(_FIXTURE_DOCUMENT, client)


def test_non_object_json_raises() -> None:
    client = _FakeLLMClient(response=json.dumps(["not", "an", "object"]))
    with pytest.raises(ProposeSplitParseError, match="must be a JSON object"):
        propose_split(_FIXTURE_DOCUMENT, client)


@pytest.mark.parametrize("missing_field", ["sections", "redirect_summary"])
def test_missing_top_level_field_raises(missing_field: str) -> None:
    payload = json.loads(json.dumps(_VALID_PAYLOAD))
    del payload[missing_field]
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(ProposeSplitParseError, match=missing_field):
        propose_split(_FIXTURE_DOCUMENT, client)


def test_sections_wrong_type_raises() -> None:
    payload = json.loads(json.dumps(_VALID_PAYLOAD))
    payload["sections"] = "should-be-a-list"
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(ProposeSplitParseError, match="sections"):
        propose_split(_FIXTURE_DOCUMENT, client)


def test_fewer_than_two_sections_raises() -> None:
    payload = json.loads(json.dumps(_VALID_PAYLOAD))
    payload["sections"] = payload["sections"][:1]
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(ProposeSplitParseError, match="at least 2"):
        propose_split(_FIXTURE_DOCUMENT, client)


@pytest.mark.parametrize("missing_field", ["new_topic_path", "start_marker"])
def test_section_missing_field_raises(missing_field: str) -> None:
    payload = json.loads(json.dumps(_VALID_PAYLOAD))
    del payload["sections"][0][missing_field]
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(ProposeSplitParseError, match=missing_field):
        propose_split(_FIXTURE_DOCUMENT, client)


def test_section_empty_new_topic_path_raises() -> None:
    payload = json.loads(json.dumps(_VALID_PAYLOAD))
    payload["sections"][1]["new_topic_path"] = ""
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(ProposeSplitParseError, match="non-empty"):
        propose_split(_FIXTURE_DOCUMENT, client)


def test_duplicate_new_topic_path_raises() -> None:
    payload = json.loads(json.dumps(_VALID_PAYLOAD))
    payload["sections"][1]["new_topic_path"] = payload["sections"][0]["new_topic_path"]
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(ProposeSplitParseError, match="duplicate"):
        propose_split(_FIXTURE_DOCUMENT, client)


def test_unresolvable_start_marker_raises() -> None:
    payload = json.loads(json.dumps(_VALID_PAYLOAD))
    payload["sections"][1]["start_marker"] = "TEXT_THAT_DOES_NOT_APPEAR_ANYWHERE"
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(ProposeSplitParseError, match="was not found"):
        propose_split(_FIXTURE_DOCUMENT, client)


def test_out_of_order_start_marker_raises() -> None:
    """The second section's marker resolves to an offset *before* (or at) the
    first section's own boundary -- e.g. re-using an earlier marker -- must be
    rejected rather than silently producing an out-of-order/zero-length range.
    """
    payload = json.loads(json.dumps(_VALID_PAYLOAD))
    payload["sections"][2]["start_marker"] = payload["sections"][1]["start_marker"]
    client = _FakeLLMClient(response=json.dumps(payload))

    with pytest.raises(ProposeSplitParseError, match="not strictly after"):
        propose_split(_FIXTURE_DOCUMENT, client)


# ---------------------------------------------------------------------------
# Non-blocking-finding regression tests (issue #45, batched Phase 4.5 cleanup:
# F2 -- non-ASCII byte-offset coverage; F3 -- substring-marker near-miss)
# ---------------------------------------------------------------------------

#: F2 fixture: a document containing 2-byte (café, überall) and 4-byte (emoji)
#: UTF-8 sequences on both sides of the resolved section boundary, so that a
#: broken/no-op `_char_offset_to_byte_offset` (one that returned the character
#: offset unchanged) would produce a boundary that either corrupts the
#: reassembled bytes or lands mid-codepoint -- either way, detectable below.
_FIXTURE_DOCUMENT_NON_ASCII = (
    "# Café overview\n\n"
    "This café section discusses überall availability across many regions. "
    "Visit our café location today \U0001f600 for details.\n\n"
    "# Überall details\n\n"
    "This section covers überall rollout timelines with emoji markers "
    "\U0001f389 sprinkled throughout.\n"
).encode("utf-8")

_VALID_PAYLOAD_NON_ASCII = {
    "sections": [
        {"new_topic_path": "cafe/Overview", "start_marker": "# Café overview"},
        {"new_topic_path": "uberall/Details", "start_marker": "# Überall details"},
    ],
    "redirect_summary": "Split café and überall overview topics.",
}


def test_propose_split_resolves_byte_offsets_for_non_ascii_content() -> None:
    """F2: `_char_offset_to_byte_offset`'s UTF-8 conversion is exercised end to
    end via `propose_split` with a fixture containing multi-byte café/überall/
    emoji sequences before, around, and after the resolved boundary -- the
    existing suite's `_FIXTURE_DOCUMENT` is pure ASCII, so char-offset and
    byte-offset happen to coincide there and would never catch a broken
    conversion.
    """
    client = _FakeLLMClient(response=json.dumps(_VALID_PAYLOAD_NON_ASCII))
    result = propose_split(_FIXTURE_DOCUMENT_NON_ASCII, client)

    assert len(result.files) == 2
    ranges = [f.section_ranges[0] for f in result.files]

    # Partition invariant holds even with multi-byte UTF-8 content.
    assert ranges[0].start == 0
    assert ranges[-1].end == len(_FIXTURE_DOCUMENT_NON_ASCII)
    assert ranges[0].end == ranges[1].start

    # Reassembling the byte ranges reproduces the exact original bytes,
    # including every multi-byte café/überall/emoji sequence.
    reassembled = b"".join(_FIXTURE_DOCUMENT_NON_ASCII[r.start : r.end] for r in ranges)
    assert reassembled == _FIXTURE_DOCUMENT_NON_ASCII

    # The text before the second boundary contains several multi-byte
    # characters, so the correct byte offset must be strictly greater than
    # the character offset of the same boundary -- the actual regression
    # check for `_char_offset_to_byte_offset`: a no-op conversion would leave
    # these equal instead.
    document_text = _FIXTURE_DOCUMENT_NON_ASCII.decode("utf-8")
    marker = _VALID_PAYLOAD_NON_ASCII["sections"][1]["start_marker"]
    char_offset_of_boundary = document_text.find(marker)
    assert char_offset_of_boundary != -1
    assert ranges[0].end > char_offset_of_boundary

    # Both resulting byte slices must themselves be valid, independently
    # decodable UTF-8 -- i.e. the byte offset never lands mid-codepoint.
    for r in ranges:
        _FIXTURE_DOCUMENT_NON_ASCII[r.start : r.end].decode("utf-8")


def test_propose_split_substring_marker_near_miss_produces_odd_but_valid_split() -> None:
    """F3: a later section's `start_marker` that happens to be a substring of
    an *earlier* section's own marker text can resolve, via the module's
    forward `str.find`, to an offset *inside* that earlier marker's own span
    -- not at the semantically-intended later boundary.

    This is not a bug relative to the module's own disclosed guarantee
    (`propose_split.py` module docstring's "Deterministic partition
    guarantee" section): the partition invariant (no gaps/overlaps, first
    range starts at 0, last ends at `len(content)`) still holds by
    construction, so no exception is raised. But the resulting split is
    semantically odd -- a very short first section, ending mid-word rather
    than where a human (or the LLM) likely intended. This test locks in and
    documents that exact, previously-untested behavior rather than leaving it
    as a silent surprise.
    """
    document = (
        b"Introduction covers the platform basics for new users in detail here. "
        b"Advanced usage covers configuration and scaling for experts."
    )
    document_text = document.decode("utf-8")
    first_marker = "Introduction covers"
    near_miss_marker = "duction covers"

    # Sanity-check the near-miss setup itself: the second section's marker is
    # a substring of the first, earlier section's own marker text.
    assert near_miss_marker in first_marker

    payload = {
        "sections": [
            {"new_topic_path": "intro/Overview", "start_marker": first_marker},
            {"new_topic_path": "advanced/Usage", "start_marker": near_miss_marker},
        ],
        "redirect_summary": "Split intro and advanced usage sections.",
    }
    client = _FakeLLMClient(response=json.dumps(payload))

    # Structurally valid: no exception, despite the semantically odd marker.
    result = propose_split(document, client)

    assert len(result.files) == 2
    ranges = [f.section_ranges[0] for f in result.files]

    # Partition invariant still holds by construction.
    assert ranges[0].start == 0
    assert ranges[-1].end == len(document)
    assert ranges[0].end == ranges[1].start
    reassembled = b"".join(document[r.start : r.end] for r in ranges)
    assert reassembled == document

    # The "odd" part: the near-miss marker resolves *inside* the first
    # section's own marker text (the module's forward `str.find` only checks
    # that the resolved offset is strictly after the previous boundary, not
    # that it's after the previous marker's own text), producing a first
    # section far shorter than even the first marker's own length.
    expected_boundary = document_text.find(near_miss_marker)
    assert expected_boundary != -1
    assert ranges[0].end == expected_boundary
    assert ranges[0].end < len(first_marker)
