"""Tests for `query.wiring`'s `GrpcSearchCandidatesClient`/`GrpcGraphNeighborsClient`/
`GrpcGetFileClient`.

Per issue #56 subtask 4.6.3.1's test spec: each class's request/response translation is
tested against a mocked `hivemind_pb2_grpc.HiveMindStub` (via `unittest.mock.MagicMock`,
mirroring `agents/ingestion/test_shortlist.py`'s `test_grpc_client_translates_request_response`
precedent exactly) -- no real gRPC channel or network call is ever created in this file.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from query.wiring import (
    GrpcGetFileClient,
    GrpcGraphNeighborsClient,
    GrpcSearchCandidatesClient,
    _import_hivemind_grpc_modules,
)


def test_search_candidates_client_translates_request_response(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    hivemind_pb2, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

    fake_response = hivemind_pb2.SearchCandidatesResponse(
        candidates=[
            hivemind_pb2.CandidateTopic(file_id=1, path="billing/InvoiceDisputes.md", score=0.9),
            hivemind_pb2.CandidateTopic(file_id=2, path="billing/PaymentDelays.md", score=0.2),
        ]
    )
    mock_stub = MagicMock()
    mock_stub.SearchCandidates.return_value = fake_response
    monkeypatch.setattr(
        hivemind_pb2_grpc, "HiveMindStub", MagicMock(return_value=mock_stub)
    )

    client = GrpcSearchCandidatesClient(channel=MagicMock())
    result = client("invoice 4521", 20)

    mock_stub.SearchCandidates.assert_called_once()
    sent_request = mock_stub.SearchCandidates.call_args.args[0]
    assert sent_request.query == "invoice 4521"
    assert sent_request.max_results == 20

    assert [(c.file_id, c.path, c.score) for c in result] == [
        (1, "billing/InvoiceDisputes.md", pytest.approx(0.9)),
        (2, "billing/PaymentDelays.md", pytest.approx(0.2)),
    ]


def test_graph_neighbors_client_translates_request_response(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    hivemind_pb2, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

    fake_response = hivemind_pb2.GraphNeighborsResponse(
        neighbors=[
            hivemind_pb2.Neighbor(
                target_file_id=3,
                type=hivemind_pb2.EdgeType.ENTITY_COOCCUR,
                weight=2,
                hop=1,
            ),
        ]
    )
    mock_stub = MagicMock()
    mock_stub.GraphNeighbors.return_value = fake_response
    monkeypatch.setattr(
        hivemind_pb2_grpc, "HiveMindStub", MagicMock(return_value=mock_stub)
    )

    client = GrpcGraphNeighborsClient(channel=MagicMock())
    result = client(2, 2)

    mock_stub.GraphNeighbors.assert_called_once()
    sent_request = mock_stub.GraphNeighbors.call_args.args[0]
    assert sent_request.file_id == 2
    assert sent_request.depth == 2

    assert len(result) == 1
    assert result[0].file_id == 3
    assert result[0].edge_type == "ENTITY_COOCCUR"
    assert result[0].weight == 2
    assert result[0].hop == 1


def test_get_file_client_translates_request_response(monkeypatch: pytest.MonkeyPatch) -> None:
    hivemind_pb2, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

    fake_response = hivemind_pb2.GetFileResponse(
        content="Invoice 4521 was disputed.".encode("utf-8"),
        version=7,
        path="billing/InvoiceDisputes.md",
    )
    mock_stub = MagicMock()
    mock_stub.GetFile.return_value = fake_response
    monkeypatch.setattr(
        hivemind_pb2_grpc, "HiveMindStub", MagicMock(return_value=mock_stub)
    )

    client = GrpcGetFileClient(channel=MagicMock())
    result = client(5)

    mock_stub.GetFile.assert_called_once()
    sent_request = mock_stub.GetFile.call_args.args[0]
    assert sent_request.file_id == 5

    assert result == ("billing/InvoiceDisputes.md", "Invoice 4521 was disputed.")


def test_get_file_client_translates_empty_path(monkeypatch: pytest.MonkeyPatch) -> None:
    """Regression test for GitHub issue #56 subtask 4.6.3.2: `GetFileResponse.path == ""`
    (proto3 zero-value, e.g. a `file_id` with no indexed path anywhere -- see
    `engine/rpc/server.go`'s `GetFile` doc comment) must translate to `""`, not raise or
    substitute a placeholder -- placeholder substitution is `pipeline._build_selected_markdown`'s
    job, not this client's.
    """
    hivemind_pb2, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

    mock_stub = MagicMock()
    mock_stub.GetFile.return_value = hivemind_pb2.GetFileResponse(
        content=b"no path indexed for this file", version=1, path=""
    )
    monkeypatch.setattr(
        hivemind_pb2_grpc, "HiveMindStub", MagicMock(return_value=mock_stub)
    )

    client = GrpcGetFileClient(channel=MagicMock())
    result = client(9)

    assert result == ("", "no path indexed for this file")


def test_get_file_client_is_a_valid_get_file_fn_for_pipeline(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """`GrpcGetFileClient` must satisfy `pipeline.GetFileFn`'s exact shape (`file_id ->
    (path, content)`), so it can be passed as `run_query_pipeline`'s `get_file` argument
    directly.
    """
    hivemind_pb2, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

    mock_stub = MagicMock()
    mock_stub.GetFile.return_value = hivemind_pb2.GetFileResponse(
        content=b"Refunds are issued within 5 business days.",
        version=1,
        path="billing/RefundPolicy.md",
    )
    monkeypatch.setattr(
        hivemind_pb2_grpc, "HiveMindStub", MagicMock(return_value=mock_stub)
    )

    client = GrpcGetFileClient(channel=MagicMock())
    path, content = client(3)

    assert isinstance(path, str)
    assert isinstance(content, str)
    assert path == "billing/RefundPolicy.md"
    assert content == "Refunds are issued within 5 business days."
