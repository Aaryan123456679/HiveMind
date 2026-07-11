"""Fixture-document test suite for the segmentation pipeline (issue #18, subtask 3.4.6).

Per the issue's own test spec: "a fixture-document test suite verifies expected
structured output shape across representative doc types" -- always runs in CI (no
live services, no `pytest.mark.skipif`). This is distinct from, and complements,
`test_segment.py`/`test_shortlist.py`/`test_segment_wiring.py`/`test_propose_split.py`
(each of which already unit-tests one module in isolation with synthetic payloads)
in two ways:

1. **Realistic multi-document fixtures.** `testdata/notes_corpus/` is a small corpus
   of related markdown "notes" spanning a few overlapping topics (billing/invoice
   disputes, billing/refund requests, an engineering on-call runbook, and an
   unrelated HR onboarding checklist), plus the pre-existing `testdata/enron_sample_*`
   and `testdata/ticket_sample_*` fixtures from issue #17's normalizers -- i.e. this
   suite exercises `RawDocument`s that actually look like the three real
   `source_type`s (`pdf`/generic-document, `email`, `ticket`) the pipeline is meant to
   ingest, not single-purpose synthetic strings invented per test.
2. **End-to-end composition, for the first time.** `test_pipeline_shortlist_segment_wiring_end_to_end`
   below is the first test anywhere in this repo that chains
   `shortlist()` -> `segment()` -> `execute_segment()` together against one document,
   proving the three already-unit-tested modules actually compose (correct data shapes
   flow from one call's return value into the next call's argument) -- something no
   existing per-module test file could catch, since each mocks its own module's direct
   dependency only.

All LLM/gRPC clients here are mocked fakes (same style as `test_segment.py`'s
`_FakeLLMClient` / `test_segment_wiring.py`'s `_FakeWiringClient`); no real network
call, no live Ollama server, no live engine. See `test_segment_live.py` for the
optional live-Ollama smoke test that this module's mocking deliberately cannot
provide (real-world response-format issues like forwarded finding F1).

`notes_corpus/` fixtures' `source_type` -- disclosed choice
-------------------------------------------------------------
`ingestion.rawdoc.RawDocument.source_type` is a closed three-value discriminant
(`"pdf" | "email" | "ticket"`) naming which *normalizer* produced a document; the
`notes_corpus/` markdown files are hand-authored fixtures, not output of any real
normalizer. `"pdf"` is used as the closest stand-in (a plain long-form document,
matching `dispatch_pdf`'s own `source_type="pdf"` for arbitrary marker-delimited page
text) -- `segment()`/`shortlist()` themselves never branch on `source_type` at all
(only `.text` is read), so this choice has no effect on the behavior under test; it is
purely for `RawDocument` construction to type-check.
"""

from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path

import pytest

from ingestion.normalize_email import normalize_email
from ingestion.normalize_ticket import normalize_ticket_json, render_ticket_blob
from ingestion.rawdoc import RawDocument
from ingestion.segment import SegmentResult, segment
from ingestion.shortlist import TopicCandidate, shortlist
from ingestion.wiring import (
    LLM_ASSERTED,
    PutSegmentResult,
    execute_segment,
)
from llm.client import LLMClient

_TESTDATA = Path(__file__).parent / "testdata"
_NOTES_CORPUS = _TESTDATA / "notes_corpus"

# ---------------------------------------------------------------------------
# Shared fakes (mirrors test_segment.py / test_segment_wiring.py conventions)
# ---------------------------------------------------------------------------


class _FakeLLMClient(LLMClient):
    """Minimal `LLMClient` stand-in returning a pre-configured canned string."""

    def __init__(self, response: str) -> None:
        self.response = response
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
        self.calls.append({"prompt": prompt, "model": model})
        return self.response


class _FakeWiringClient:
    """Minimal fake satisfying `SegmentWiringClient` structurally.

    Mirrors `test_segment_wiring.py`'s `_FakeWiringClient`: records every call,
    always succeeds, and allocates incrementing fileIDs for `CREATE_NEW` segments.
    """

    def __init__(self) -> None:
        self.put_segment_calls: list[tuple[int, bytes]] = []
        self.put_edge_calls: list[tuple[int, int, str]] = []
        self.indexed_entities: list[tuple[str, int]] = []
        self._next_file_id = 100

    def put_segment(self, file_id: int, content: bytes) -> PutSegmentResult:
        self.put_segment_calls.append((file_id, content))
        if file_id == 0:
            file_id = self._next_file_id
            self._next_file_id += 1
        return PutSegmentResult(file_id=file_id, new_version=1)

    def lookup_entity_files(self, entity: str) -> tuple[int, ...]:
        return ()

    def index_entity(self, entity: str, file_id: int) -> None:
        self.indexed_entities.append((entity, file_id))

    def put_edge(
        self, source_file_id: int, target_file_id: int, edge_type: str, *, occurrence_weight: int = 1
    ) -> None:
        self.put_edge_calls.append((source_file_id, target_file_id, edge_type))


def _email_doc(path: Path, doc_id: str) -> RawDocument:
    """Build an email `RawDocument` directly via `normalize_email`, not
    `ingestion.dispatch.dispatch_email` -- `ingestion.dispatch` unconditionally
    imports `ingestion.normalize_pdf`, which imports `fitz` (pymupdf) at module
    import time; this test module's `email`/`ticket` fixtures should not require
    pymupdf to be installed just to collect, so they call the email/ticket
    normalizers directly instead of going through `dispatch`.
    """
    fields = normalize_email(path)
    return RawDocument(
        id=doc_id,
        source_type="email",
        text=fields.body,
        structured_fields={},
        timestamp=datetime(2026, 3, 20, tzinfo=timezone.utc),
    )


def _ticket_doc(data: dict, doc_id: str) -> RawDocument:
    """Build a ticket `RawDocument` directly via `normalize_ticket_json` -- see
    `_email_doc`'s docstring for why `ingestion.dispatch` is deliberately not used.
    """
    fields = normalize_ticket_json(data)
    return RawDocument(
        id=doc_id,
        source_type="ticket",
        text=render_ticket_blob(fields),
        structured_fields={},
        timestamp=datetime(2026, 3, 20, tzinfo=timezone.utc),
    )


def _load_note(name: str, doc_id: str) -> RawDocument:
    text = (_NOTES_CORPUS / name).read_text(encoding="utf-8")
    return RawDocument(
        id=doc_id,
        source_type="pdf",  # see module docstring's disclosed choice
        text=text,
        structured_fields={},
        timestamp=datetime(2026, 3, 20, tzinfo=timezone.utc),
    )


def _valid_segment_payload(
    *,
    topic_action: str = "APPEND_EXISTING",
    target_topic: str = "billing/InvoiceDisputes",
    new_topic_path: str = "",
    entities: list[str] | None = None,
    related_topics: list[str] | None = None,
) -> dict:
    return {
        "topic_action": topic_action,
        "target_topic": target_topic,
        "new_topic_path": new_topic_path,
        "content_markdown": "## Segment\n\nSome filed content.",
        "entities": entities if entities is not None else [],
        "related_topics": related_topics if related_topics is not None else [],
    }


# ---------------------------------------------------------------------------
# Fixture suite: structured-output shape across representative doc types
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "doc_factory,expected_source_type",
    [
        (lambda: _load_note("billing_invoice_dispute.md", "note-1"), "pdf"),
        (
            lambda: _email_doc(_TESTDATA / "enron_sample_1.txt", "email-1"),
            "email",
        ),
        (
            lambda: _ticket_doc(
                json.loads((_TESTDATA / "ticket_sample_1.json").read_text()),
                "ticket-1",
            ),
            "ticket",
        ),
    ],
)
def test_segment_returns_expected_shape_across_doc_types(
    doc_factory, expected_source_type: str
) -> None:
    """`segment()` on each representative doc type returns a `SegmentResult` with
    the LLD's exact flat shape (`topic_action, target_topic, new_topic_path,
    content_markdown, entities, related_topics`), regardless of which normalizer
    produced the input `RawDocument`.
    """
    doc = doc_factory()
    assert doc.source_type == expected_source_type

    payload = _valid_segment_payload(
        entities=["some-entity"], related_topics=["some/topic"]
    )
    client = _FakeLLMClient(response=json.dumps(payload))
    shortlist_candidates = [
        TopicCandidate(file_id=1, path="billing/InvoiceDisputes", score=1.0)
    ]

    result = segment(doc, shortlist_candidates, client)

    assert isinstance(result, SegmentResult)
    assert result.topic_action in ("APPEND_EXISTING", "CREATE_NEW")
    assert isinstance(result.target_topic, str)
    assert isinstance(result.new_topic_path, str)
    assert isinstance(result.content_markdown, str) and result.content_markdown
    assert result.entities == ["some-entity"]
    assert result.related_topics == ["some/topic"]


def test_segment_across_notes_corpus_documents() -> None:
    """Every document in `testdata/notes_corpus/` segments to a well-formed
    `SegmentResult` (fixture-shape coverage across the whole small corpus, not just
    one representative file).
    """
    for path in sorted(_NOTES_CORPUS.glob("*.md")):
        doc = _load_note(path.name, doc_id=path.stem)
        client = _FakeLLMClient(response=json.dumps(_valid_segment_payload()))
        shortlist_candidates = [
            TopicCandidate(file_id=1, path="billing/InvoiceDisputes", score=1.0)
        ]

        result = segment(doc, shortlist_candidates, client)

        assert isinstance(result, SegmentResult)
        assert path.stem in doc.id


# ---------------------------------------------------------------------------
# End-to-end pipeline composition: shortlist -> segment -> wiring
# ---------------------------------------------------------------------------


def test_pipeline_shortlist_segment_wiring_end_to_end() -> None:
    """First end-to-end test of the pipeline's actual composition.

    Chains, against the realistic `notes_corpus` fixtures:

    1. `shortlist()` -- BM25-ranks a mocked `SearchCandidates` pool (drawn from the
       corpus's own topic paths) against the incoming document's text, returning a
       bounded, relevant subset.
    2. `segment()` -- takes that shortlist plus the document, calls a mocked
       `LLMClient` (canned response asserting `APPEND_EXISTING` to the top-ranked
       shortlist candidate, with entities/related_topics), and returns a validated
       `SegmentResult`.
    3. `execute_segment()` -- takes that `SegmentResult`, calls a mocked
       `SegmentWiringClient`, and wires `PutSegment` + `ENTITY_COOCCUR` +
       `LLM_ASSERTED` edges.

    Each step's real return type/shape is threaded into the next step's real
    argument -- no step's output is faked or reshaped by the test itself -- so this
    is a genuine composition check, not three independent mocked calls glued together
    by assertion alone.
    """
    doc = _load_note("billing_invoice_dispute.md", doc_id="note-invoice-dispute")

    # Step 1: shortlist(), against a mocked SearchCandidates pool drawn from the
    # corpus's own topics (an irrelevant HR topic included to prove BM25 ranks it
    # below the relevant billing topics, not just that it's present in the pool).
    pool = [
        TopicCandidate(file_id=10, path="billing/InvoiceDisputes", score=1.0),
        TopicCandidate(file_id=11, path="billing/RefundRequests", score=1.0),
        TopicCandidate(file_id=12, path="hr/OnboardingChecklist", score=1.0),
    ]

    def fake_search_candidates(query: str, max_results: int) -> list[TopicCandidate]:
        assert query == ""
        return pool[:max_results]

    shortlist_result = shortlist(doc.text, fake_search_candidates, top_k=2, pool_size=10)

    # Bounded to top_k, and both billing topics (the actually-relevant ones) outrank
    # the unrelated HR topic -- the specific ordering between the two billing topics
    # is a BM25 scoring detail this test does not pin down, only that HR loses.
    assert len(shortlist_result) <= 2
    shortlisted_paths = [candidate.path for candidate in shortlist_result]
    assert "hr/OnboardingChecklist" not in shortlisted_paths
    assert shortlisted_paths[0].startswith("billing/")

    top_candidate = shortlist_result[0]
    # The *other* billing topic (not the one segment() is about to append to) --
    # used below as `related_topics`, so the LLM_ASSERTED edge created is never a
    # (skipped) self-edge regardless of which billing topic BM25 happened to rank
    # first.
    other_billing_candidate = next(
        candidate
        for candidate in pool
        if candidate.path.startswith("billing/") and candidate.path != top_candidate.path
    )

    # Step 2: segment(), against the shortlist just produced (not a hand-built one).
    segment_payload = _valid_segment_payload(
        topic_action="APPEND_EXISTING",
        target_topic=top_candidate.path,
        entities=["Priya Nair", "Marcus Webb"],
        related_topics=[other_billing_candidate.path],
    )
    llm_client = _FakeLLMClient(response=json.dumps(segment_payload))
    segment_result = segment(doc, shortlist_result, llm_client)

    assert segment_result.topic_action == "APPEND_EXISTING"
    assert segment_result.target_topic == top_candidate.path
    assert segment_result.entities == ["Priya Nair", "Marcus Webb"]

    # Confirm the shortlist's own paths actually reached the LLM prompt (proving
    # step 1's real output, not a hardcoded string, is what flowed into step 2).
    prompt = llm_client.calls[0]["prompt"]
    assert top_candidate.path in prompt

    # Step 3: execute_segment(), against the segment_result just produced.
    wiring_client = _FakeWiringClient()

    def resolve_topic_file_id(path: str) -> int | None:
        for candidate in pool:
            if candidate.path == path:
                return candidate.file_id
        return None

    execution_result = execute_segment(
        segment_result, wiring_client, resolve_topic_file_id=resolve_topic_file_id
    )

    assert execution_result.file_id == top_candidate.file_id
    assert wiring_client.put_segment_calls == [
        (top_candidate.file_id, segment_result.content_markdown.encode("utf-8"))
    ]
    assert wiring_client.indexed_entities == [
        ("Priya Nair", top_candidate.file_id),
        ("Marcus Webb", top_candidate.file_id),
    ]
    # related_topics -> LLM_ASSERTED edge to the other billing topic's fileID.
    assert (
        top_candidate.file_id,
        other_billing_candidate.file_id,
        LLM_ASSERTED,
    ) in wiring_client.put_edge_calls
    assert list(execution_result.errors) == []
