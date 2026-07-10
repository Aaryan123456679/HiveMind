"""Tests for `ingestion.shortlist.shortlist` and `GrpcSearchCandidatesClient`.

Per issue #18 subtask 3.4.2's test spec: `SearchCandidates` is mocked entirely (a
plain Python callable stands in for the RPC; no real gRPC channel or network call is
ever created in this file). Assertions cover:

- the returned shortlist size is bounded regardless of how large the mocked
  candidate pool is;
- using fixture documents/topics, topics genuinely relevant to the document content
  rank ahead of irrelevant ones in the returned shortlist;
- `search_candidates` is invoked with the expected bounded-pool request shape
  (`query=""`, `max_results=pool_size`);
- the disclosed gRPC-client gap-fill (`GrpcSearchCandidatesClient`) correctly
  translates request/response shapes, using a mock stand-in for `grpc.Channel` /
  the generated stub -- still no real network call.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from ingestion.shortlist import (
    DEFAULT_POOL_SIZE,
    GrpcSearchCandidatesClient,
    TopicCandidate,
    shortlist,
)

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

#: Topics genuinely relevant to `_INVOICE_DOCUMENT_TEXT` below.
_RELEVANT_TOPIC_PATHS = [
    "billing/InvoiceDisputes",
    "billing/PaymentDelays",
    "billing/RefundRequests",
]

#: Topics with no meaningful overlap with `_INVOICE_DOCUMENT_TEXT`.
_IRRELEVANT_TOPIC_PATHS = [
    "hr/Onboarding",
    "hr/BenefitsEnrollment",
    "legal/NDATemplates",
    "legal/VendorContracts",
    "engineering/DeploymentRunbooks",
    "engineering/IncidentPostmortems",
    "marketing/CampaignBriefs",
    "marketing/BrandGuidelines",
    "facilities/OfficeMoveRequests",
]

_INVOICE_DOCUMENT_TEXT = """
Subject: Invoice #4521 payment delay and refund request

The customer disputes invoice 4521, citing a duplicate charge. They are
requesting a refund for the disputed invoice amount and have flagged that
the payment was delayed by their bank. Please review the billing records
and process the refund for this invoice dispute as soon as possible.
"""


def _fixture_pool() -> list[TopicCandidate]:
    paths = _RELEVANT_TOPIC_PATHS + _IRRELEVANT_TOPIC_PATHS
    return [
        TopicCandidate(file_id=i, path=path, score=1.0) for i, path in enumerate(paths)
    ]


def _mock_search_candidates(pool: list[TopicCandidate]):
    calls = []

    def fn(query: str, max_results: int) -> list[TopicCandidate]:
        calls.append((query, max_results))
        return pool

    fn.calls = calls  # type: ignore[attr-defined]
    return fn


# ---------------------------------------------------------------------------
# Bounded size
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("top_k", [1, 3, 8])
def test_shortlist_size_is_bounded_by_top_k(top_k: int) -> None:
    pool = _fixture_pool()
    assert len(pool) > top_k  # pool is deliberately much larger than top_k

    result = shortlist(_INVOICE_DOCUMENT_TEXT, _mock_search_candidates(pool), top_k=top_k)

    assert len(result) == top_k


def test_shortlist_never_exceeds_pool_size_when_pool_smaller_than_top_k() -> None:
    pool = _fixture_pool()[:2]

    result = shortlist(_INVOICE_DOCUMENT_TEXT, _mock_search_candidates(pool), top_k=8)

    assert len(result) == len(pool)


def test_shortlist_is_bounded_even_with_a_very_large_mocked_pool() -> None:
    large_pool = [
        TopicCandidate(file_id=i, path=f"misc/Topic{i}", score=1.0) for i in range(5000)
    ]
    large_pool.append(TopicCandidate(file_id=9999, path="billing/InvoiceDisputes", score=1.0))

    result = shortlist(_INVOICE_DOCUMENT_TEXT, _mock_search_candidates(large_pool), top_k=5)

    assert len(result) == 5


def test_shortlist_returns_empty_list_when_top_k_is_zero() -> None:
    result = shortlist(_INVOICE_DOCUMENT_TEXT, _mock_search_candidates(_fixture_pool()), top_k=0)
    assert result == []


def test_shortlist_returns_empty_list_when_pool_is_empty() -> None:
    result = shortlist(_INVOICE_DOCUMENT_TEXT, _mock_search_candidates([]), top_k=8)
    assert result == []


@pytest.mark.parametrize("bad_kwargs", [{"top_k": -1}, {"pool_size": -1}])
def test_shortlist_rejects_negative_bounds(bad_kwargs: dict) -> None:
    with pytest.raises(ValueError):
        shortlist(_INVOICE_DOCUMENT_TEXT, _mock_search_candidates(_fixture_pool()), **bad_kwargs)


# ---------------------------------------------------------------------------
# Relevance ranking
# ---------------------------------------------------------------------------


def test_relevant_topics_rank_ahead_of_irrelevant_ones() -> None:
    pool = _fixture_pool()

    result = shortlist(_INVOICE_DOCUMENT_TEXT, _mock_search_candidates(pool), top_k=len(pool))

    result_paths = [c.path for c in result]
    relevant_ranks = [result_paths.index(p) for p in _RELEVANT_TOPIC_PATHS]
    irrelevant_ranks = [result_paths.index(p) for p in _IRRELEVANT_TOPIC_PATHS]

    assert max(relevant_ranks) < min(irrelevant_ranks)


def test_relevant_topics_survive_a_small_top_k_truncation() -> None:
    pool = _fixture_pool()

    result = shortlist(_INVOICE_DOCUMENT_TEXT, _mock_search_candidates(pool), top_k=3)

    result_paths = {c.path for c in result}
    assert result_paths == set(_RELEVANT_TOPIC_PATHS)


def test_shortlist_scores_are_descending() -> None:
    pool = _fixture_pool()

    result = shortlist(_INVOICE_DOCUMENT_TEXT, _mock_search_candidates(pool), top_k=len(pool))

    scores = [c.score for c in result]
    assert scores == sorted(scores, reverse=True)


# ---------------------------------------------------------------------------
# SearchCandidates call shape (mocked -- no real gRPC/network call)
# ---------------------------------------------------------------------------


def test_shortlist_calls_search_candidates_with_broad_pool_query() -> None:
    pool = _fixture_pool()
    mock_fn = _mock_search_candidates(pool)

    shortlist(_INVOICE_DOCUMENT_TEXT, mock_fn, top_k=5, pool_size=50)

    assert mock_fn.calls == [("", 50)]


def test_shortlist_uses_default_pool_size_when_unspecified() -> None:
    pool = _fixture_pool()
    mock_fn = _mock_search_candidates(pool)

    shortlist(_INVOICE_DOCUMENT_TEXT, mock_fn, top_k=5)

    assert mock_fn.calls == [("", DEFAULT_POOL_SIZE)]


# ---------------------------------------------------------------------------
# GrpcSearchCandidatesClient (disclosed gap-fill) -- mocked stub, no real gRPC
# ---------------------------------------------------------------------------


def test_grpc_client_translates_request_response(monkeypatch: pytest.MonkeyPatch) -> None:
    from ingestion.shortlist import _import_hivemind_grpc_modules

    hivemind_pb2, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

    fake_response = hivemind_pb2.SearchCandidatesResponse(
        candidates=[
            hivemind_pb2.CandidateTopic(file_id=1, path="billing/InvoiceDisputes", score=0.5),
            hivemind_pb2.CandidateTopic(file_id=2, path="hr/Onboarding", score=0.5),
        ]
    )

    mock_stub = MagicMock()
    mock_stub.SearchCandidates.return_value = fake_response
    monkeypatch.setattr(
        hivemind_pb2_grpc, "HiveMindStub", MagicMock(return_value=mock_stub)
    )

    client = GrpcSearchCandidatesClient(channel=MagicMock())
    result = client("", 200)

    mock_stub.SearchCandidates.assert_called_once()
    sent_request = mock_stub.SearchCandidates.call_args.args[0]
    assert sent_request.query == ""
    assert sent_request.max_results == 200

    assert result == [
        TopicCandidate(file_id=1, path="billing/InvoiceDisputes", score=0.5),
        TopicCandidate(file_id=2, path="hr/Onboarding", score=0.5),
    ]


def test_grpc_client_is_a_valid_search_candidates_fn_for_shortlist(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """`GrpcSearchCandidatesClient` itself satisfies `SearchCandidatesFn` and can be
    passed straight into `shortlist()` -- still with the stub mocked, no real gRPC."""
    from ingestion.shortlist import _import_hivemind_grpc_modules

    hivemind_pb2, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

    fake_response = hivemind_pb2.SearchCandidatesResponse(
        candidates=[
            hivemind_pb2.CandidateTopic(file_id=i, path=path, score=1.0)
            for i, path in enumerate(_RELEVANT_TOPIC_PATHS + _IRRELEVANT_TOPIC_PATHS)
        ]
    )
    mock_stub = MagicMock()
    mock_stub.SearchCandidates.return_value = fake_response
    monkeypatch.setattr(
        hivemind_pb2_grpc, "HiveMindStub", MagicMock(return_value=mock_stub)
    )

    client = GrpcSearchCandidatesClient(channel=MagicMock())
    result = shortlist(_INVOICE_DOCUMENT_TEXT, client, top_k=3)

    assert len(result) == 3
    assert {c.path for c in result} == set(_RELEVANT_TOPIC_PATHS)
