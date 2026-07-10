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
