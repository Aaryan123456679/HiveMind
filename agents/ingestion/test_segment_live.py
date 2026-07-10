"""Optional live-Ollama smoke test for `segment()`/`propose_split()` (issue #18,
subtask 3.4.6's second test spec item).

Per the issue's test spec: "an optional (skippable if Ollama isn't running locally)
smoke test exercises the real Ollama model end-to-end." Every test in this module is
skipped automatically (via `pytest.mark.skipif`, not an error/failure) unless a real
Ollama server is reachable at `OllamaClient`'s default base URL -- normal `pytest`
runs and CI never depend on a live Ollama server; run manually with
``ollama serve`` (and ``ollama pull llama3.1:8b``, or override `HIVEMIND_OLLAMA_MODEL`)
running locally to actually exercise these.

Why this exists alongside the fully-mocked `test_segment_fixtures.py`
-----------------------------------------------------------------------
Every other test in `agents/ingestion/`/`agents/llm/` mocks `LLMClient.complete()`
with a hand-written canned string -- which can only prove "if the model returns
*exactly this shape*, parsing works," never "the real model's response actually
matches the shape the prompt asked for." Forwarded finding F1
(`.cdr/index/regression.jsonl`) was exactly this kind of gap: a real Ollama-backed
model wrapping JSON in a markdown code fence despite being told not to, which no
mocked test could have caught until someone thought to construct that exact
malformed-fence fixture by hand (which `test_segment.py` and `test_propose_split.py`
now both do). This module is the live counterpart: it calls the real model and
parses whatever it actually returns, so a *new*, not-yet-imagined response-format
quirk would surface here first, before a mocked fixture is even written for it.

Availability check -- disclosed design
-----------------------------------------
`_ollama_is_reachable()` does a short-timeout plain HTTP GET against
`{base_url}/` (Ollama's root endpoint responds `200 Ollama is running` with no auth
needed) and treats any connection failure/timeout as "not available" -- this is a
liveness probe only, not a correctness check; if it responds we still let the actual
`OllamaClient` calls below raise/fail normally on any real error.
"""

from __future__ import annotations

import os
from datetime import datetime, timezone

import httpx
import pytest

from ingestion.rawdoc import RawDocument
from ingestion.segment import SegmentResult, segment
from ingestion.shortlist import TopicCandidate
from ingestion.propose_split import ProposeSplitResult, propose_split
from llm.ollama_client import DEFAULT_BASE_URL, OllamaClient

_MODEL = os.environ.get("HIVEMIND_OLLAMA_MODEL", "llama3.1:8b")
_PROBE_TIMEOUT_SECONDS = 1.0


def _ollama_is_reachable(base_url: str = DEFAULT_BASE_URL) -> bool:
    """Return True iff an Ollama server responds at `base_url` within a short timeout.

    See module docstring's "Availability check" section. Any exception (connection
    refused, DNS failure, timeout, ...) is treated as "not reachable" -- this probe
    must never itself raise, or every normal (no-Ollama) test run would error instead
    of skipping.
    """
    try:
        response = httpx.get(base_url, timeout=_PROBE_TIMEOUT_SECONDS)
        return response.status_code == 200
    except httpx.HTTPError:
        return False


_SKIP_REASON = (
    f"no Ollama server reachable at {DEFAULT_BASE_URL} -- this smoke test is "
    "optional and skipped by default; run `ollama serve` (+ `ollama pull "
    f"{_MODEL}`) locally to exercise it"
)

pytestmark = pytest.mark.skipif(not _ollama_is_reachable(), reason=_SKIP_REASON)


def _make_doc() -> RawDocument:
    return RawDocument(
        id="live-smoke-doc-1",
        source_type="ticket",
        text=(
            "Ticket TCK-9001: Customer reports the login page returns a 500 "
            "error after submitting credentials. Assigned to support-eng-1. "
            "Priority: high. Category: authentication."
        ),
        structured_fields={},
        timestamp=datetime(2026, 7, 1, tzinfo=timezone.utc),
    )


@pytest.fixture()
def ollama_client() -> OllamaClient:
    return OllamaClient(model=_MODEL)


def test_segment_against_real_ollama_model(ollama_client: OllamaClient) -> None:
    """`segment()` against a real local Ollama model returns a well-formed
    `SegmentResult` -- catching real-world response-format issues (like F1's
    markdown-code-fence wrapping) that mocked tests structurally cannot.
    """
    shortlist_candidates = [
        TopicCandidate(file_id=1, path="support/AuthenticationIssues", score=1.0),
        TopicCandidate(file_id=2, path="support/BillingIssues", score=0.5),
    ]

    result = segment(_make_doc(), shortlist_candidates, ollama_client, temperature=0.0)

    assert isinstance(result, SegmentResult)
    assert result.topic_action in ("APPEND_EXISTING", "CREATE_NEW")
    assert isinstance(result.content_markdown, str)
    assert result.content_markdown.strip() != ""


def test_propose_split_against_real_ollama_model(ollama_client: OllamaClient) -> None:
    """`propose_split()` against a real local Ollama model returns a well-formed,
    gap-free/overlap-free partition of the source content.
    """
    long_document = (
        "# Q1 platform incidents\n\n"
        "## Incident 1: Stripe webhook retry storm\n"
        "On 2026-03-18 the billing webhook handler retried a request that had "
        "already succeeded, causing duplicate charges for several customers. "
        "Root cause: no idempotency key check before applying a charge. Fixed "
        "2026-03-20 by Marcus Webb.\n\n"
        "## Incident 2: Search index staleness\n"
        "On 2026-03-25 the topic search index fell behind by several hours due "
        "to a stuck compaction job in engine/graph. Fixed by restarting the "
        "compaction worker and adding a staleness alert.\n\n"
        "## Incident 3: Onboarding flow outage\n"
        "On 2026-03-29 new-hire Okta SSO provisioning failed for two days due "
        "to an expired service-account credential. Fixed by rotating the "
        "credential and adding an expiry alert 30 days out.\n"
    )

    result = propose_split(long_document.encode("utf-8"), ollama_client, temperature=0.0)

    assert isinstance(result, ProposeSplitResult)
    assert len(result.files) >= 2

    # Partition invariant: ranges cover the source with no gaps or overlaps.
    all_ranges = sorted(
        (rng.start, rng.end) for file in result.files for rng in file.section_ranges
    )
    assert all_ranges[0][0] == 0
    assert all_ranges[-1][1] == len(long_document)
    for (start_a, end_a), (start_b, end_b) in zip(all_ranges, all_ranges[1:]):
        assert end_a == start_b
