"""Tests for `query.server.QueryRunQueryServicer` (GitHub issue #56 subtask 4.6.3.2's
`RunQuery` gRPC server -- the Python-side half of `api/main.go`'s
`notImplementedPipeline` fix).

Per this subtask's own established convention (`test_wiring.py`'s
`test_search_candidates_client_translates_request_response`, mirroring
`agents/ingestion/test_shortlist.py`): `hivemind_pb2_grpc.HiveMindStub` is mocked via
`unittest.mock.MagicMock` so no real gRPC channel or network call is ever created. This lets
`QueryRunQueryServicer.RunQuery`'s own request/response translation and full
`run_query_pipeline` wiring be tested without standing up a real engine process.
"""

from __future__ import annotations

import json
from unittest.mock import MagicMock

import pytest

from llm.client import LLMClient
from query.server import QueryRunQueryServicer
from query.wiring import _import_hivemind_grpc_modules


class _FakeLLMClient(LLMClient):
    """Fake `LLMClient` returning canned JSON responses in call order -- one for
    intent-refinement, one for synthesis. Mirrors `test_pipeline.py`'s own
    `_FakeLLMClient` convention."""

    def __init__(self, responses: list[str]) -> None:
        self._responses = list(responses)
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
        self.calls.append({"prompt": prompt})
        index = len(self.calls) - 1
        assert index < len(self._responses), "more LLM calls than canned responses supplied"
        return self._responses[index]


def _mock_stub_for_engine(monkeypatch: pytest.MonkeyPatch):
    """Monkeypatch `hivemind_pb2_grpc.HiveMindStub` so every `wiring.py` client class
    constructed against any channel shares one `MagicMock` stub instance, and return it so
    callers can configure `SearchCandidates`/`GraphNeighbors`/`GetFile`'s return values."""
    _, hivemind_pb2_grpc = _import_hivemind_grpc_modules()
    mock_stub = MagicMock()
    monkeypatch.setattr(hivemind_pb2_grpc, "HiveMindStub", MagicMock(return_value=mock_stub))
    return mock_stub


_INTENT_RESPONSE = json.dumps(
    {
        "refined_intent": "What is the process for disputing a duplicate invoice charge?",
        "entities": ["invoice"],
        "query_type": "factual_lookup",
    }
)


def test_run_query_returns_synthesized_answer_and_citations(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    hivemind_pb2, _ = _import_hivemind_grpc_modules()
    mock_stub = _mock_stub_for_engine(monkeypatch)

    billing_path = "billing/InvoiceDisputes.md"
    mock_stub.SearchCandidates.return_value = hivemind_pb2.SearchCandidatesResponse(
        candidates=[hivemind_pb2.CandidateTopic(file_id=1, path=billing_path, score=0.9)]
    )
    mock_stub.GetFile.return_value = hivemind_pb2.GetFileResponse(
        content=b"Open a dispute ticket within 30 days.", version=1, path=billing_path
    )

    synthesis_response = json.dumps(
        {
            "answer": f"Open a dispute ticket within 30 days [{billing_path}].",
            "citations": [billing_path],
        }
    )
    llm_client = _FakeLLMClient([_INTENT_RESPONSE, synthesis_response])

    servicer = QueryRunQueryServicer(engine_channel=MagicMock(), llm_client=llm_client)

    request = hivemind_pb2.RunQueryRequest(
        query="My customer was charged twice for the same invoice -- how do we dispute it?",
        history=[],
    )
    response = servicer.RunQuery(request, context=None)

    assert response.answer == f"Open a dispute ticket within 30 days [{billing_path}]."
    assert list(response.citations) == [billing_path]

    # Confirm this really went through the engine RPCs (SearchCandidates/GetFile), not a
    # bypassed/mocked pipeline shortcut.
    mock_stub.SearchCandidates.assert_called_once()
    mock_stub.GetFile.assert_called()


def test_run_query_propagates_pipeline_errors(monkeypatch: pytest.MonkeyPatch) -> None:
    """A malformed LLM response (unparseable JSON) causes `refine_intent` to raise
    `IntentRefinerParseError`; `RunQuery` does not swallow it (see `server.py`'s `RunQuery`
    doc comment: uncaught exceptions abort the RPC with `codes.UNKNOWN`, which is
    intentional -- no additional error-translation layer is added here)."""
    from query.intent_refiner import IntentRefinerParseError

    hivemind_pb2, _ = _import_hivemind_grpc_modules()
    mock_stub = _mock_stub_for_engine(monkeypatch)
    mock_stub.SearchCandidates.return_value = hivemind_pb2.SearchCandidatesResponse(candidates=[])

    llm_client = _FakeLLMClient(["not valid json"])
    servicer = QueryRunQueryServicer(engine_channel=MagicMock(), llm_client=llm_client)

    request = hivemind_pb2.RunQueryRequest(query="anything", history=[])

    with pytest.raises(IntentRefinerParseError):
        servicer.RunQuery(request, context=None)
