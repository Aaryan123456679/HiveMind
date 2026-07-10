"""Tests for `ingestion.wiring.execute_segment` and `GrpcPutSegmentClient`.

Per issue #18 subtask 3.4.4's test spec: the engine RPC client is mocked entirely (a
plain fake implementing `SegmentWiringClient` structurally; no real gRPC channel or
network call anywhere in this file). Assertions cover:

- `PutSegment` is called with the correct `file_id`/`content` shape for both
  `CREATE_NEW` (`file_id=0`) and `APPEND_EXISTING` (resolved fileID) segments;
- an unresolvable `target_topic` on an `APPEND_EXISTING` segment raises
  `TopicNotFoundError` *before* any RPC call is made (zero calls recorded);
- a `put_segment` failure propagates unwrapped and no edge-creation calls are
  attempted (fail-fast semantics);
- `entities` create `ENTITY_COOCCUR` edges to *other* files already indexed under the
  same entity (cross-file co-occurrence, not entity-to-entity), self-edges are
  skipped, and every entity is registered into `entity.idx` via `index_entity`;
- `related_topics` create `LLM_ASSERTED` edges to their resolved fileID, self-edges
  are skipped, and an unresolvable related topic is collected into `errors` rather
  than raised;
- one edge-creation failure is collected into `errors` and does not prevent other
  entity/related-topic operations in the same segment from being attempted
  (best-effort-with-collection semantics);
- duplicate entities/related_topics within one segment are each processed exactly
  once;
- `GrpcPutSegmentClient` correctly translates request/response shapes using a mock
  stand-in for `grpc.Channel`/the generated stub -- still no real network call.
"""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from ingestion.segment import SegmentResult
from ingestion.wiring import (
    ENTITY_COOCCUR,
    LLM_ASSERTED,
    GrpcPutSegmentClient,
    PutSegmentResult,
    TopicNotFoundError,
    execute_segment,
)

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


class _FakeWiringClient:
    """Minimal fake satisfying `SegmentWiringClient` structurally (no ABC/Protocol
    subclassing required -- structural typing, matching how `execute_segment` itself
    only relies on duck-typed method calls).

    Records every call for assertions. `lookup_entity_files` responses and per-entity
    /per-related-topic `put_edge` failures are configurable via constructor args, to
    exercise both the happy path and the best-effort error-collection path.
    """

    def __init__(
        self,
        *,
        next_file_id: int = 42,
        new_version: int = 1,
        entity_files: dict[str, list[int]] | None = None,
        put_segment_error: Exception | None = None,
        put_edge_errors: dict[tuple[int, int, str], Exception] | None = None,
    ) -> None:
        self._next_file_id = next_file_id
        self._new_version = new_version
        self._entity_files = entity_files or {}
        self._put_segment_error = put_segment_error
        self._put_edge_errors = put_edge_errors or {}

        self.put_segment_calls: list[tuple[int, bytes]] = []
        self.lookup_entity_files_calls: list[str] = []
        self.index_entity_calls: list[tuple[str, int]] = []
        self.put_edge_calls: list[tuple[int, int, str, int]] = []

    def put_segment(self, file_id: int, content: bytes) -> PutSegmentResult:
        self.put_segment_calls.append((file_id, content))
        if self._put_segment_error is not None:
            raise self._put_segment_error
        resolved_file_id = file_id if file_id != 0 else self._next_file_id
        return PutSegmentResult(file_id=resolved_file_id, new_version=self._new_version)

    def lookup_entity_files(self, entity: str) -> list[int]:
        self.lookup_entity_files_calls.append(entity)
        return list(self._entity_files.get(entity, []))

    def index_entity(self, entity: str, file_id: int) -> None:
        self.index_entity_calls.append((entity, file_id))

    def put_edge(
        self, source_file_id: int, target_file_id: int, edge_type: str, *, weight_delta: int = 1
    ) -> None:
        self.put_edge_calls.append((source_file_id, target_file_id, edge_type, weight_delta))
        error = self._put_edge_errors.get((source_file_id, target_file_id, edge_type))
        if error is not None:
            raise error


def _segment(
    *,
    topic_action: str = "CREATE_NEW",
    target_topic: str = "",
    new_topic_path: str = "billing/InvoiceDisputes",
    content_markdown: str = "# Invoice disputes\nSome content.",
    entities: list[str] | None = None,
    related_topics: list[str] | None = None,
) -> SegmentResult:
    return SegmentResult(
        topic_action=topic_action,
        target_topic=target_topic,
        new_topic_path=new_topic_path,
        content_markdown=content_markdown,
        entities=entities if entities is not None else [],
        related_topics=related_topics if related_topics is not None else [],
    )


def _no_resolver(_path: str) -> int | None:
    return None


# ---------------------------------------------------------------------------
# PutSegment execution
# ---------------------------------------------------------------------------


def test_create_new_calls_put_segment_with_zero_file_id():
    client = _FakeWiringClient(next_file_id=99, new_version=1)
    segment_result = _segment(topic_action="CREATE_NEW", new_topic_path="a/b", content_markdown="hello")

    result = execute_segment(segment_result, client, resolve_topic_file_id=_no_resolver)

    assert client.put_segment_calls == [(0, b"hello")]
    assert result.file_id == 99
    assert result.new_version == 1


def test_append_existing_calls_put_segment_with_resolved_file_id():
    client = _FakeWiringClient(new_version=7)
    segment_result = _segment(
        topic_action="APPEND_EXISTING", target_topic="billing/InvoiceDisputes", content_markdown="more"
    )

    def resolver(path: str) -> int | None:
        return 5 if path == "billing/InvoiceDisputes" else None

    result = execute_segment(segment_result, client, resolve_topic_file_id=resolver)

    assert client.put_segment_calls == [(5, b"more")]
    assert result.file_id == 5
    assert result.new_version == 7


def test_unresolvable_target_topic_raises_before_any_rpc_call():
    client = _FakeWiringClient()
    segment_result = _segment(topic_action="APPEND_EXISTING", target_topic="unknown/topic")

    with pytest.raises(TopicNotFoundError, match="unknown/topic"):
        execute_segment(segment_result, client, resolve_topic_file_id=_no_resolver)

    assert client.put_segment_calls == []
    assert client.put_edge_calls == []
    assert client.lookup_entity_files_calls == []


def test_put_segment_failure_propagates_and_skips_edges():
    client = _FakeWiringClient(put_segment_error=RuntimeError("engine unavailable"))
    segment_result = _segment(entities=["Acme Corp"], related_topics=["billing/Refunds"])

    with pytest.raises(RuntimeError, match="engine unavailable"):
        execute_segment(segment_result, client, resolve_topic_file_id=_no_resolver)

    assert client.put_segment_calls == [(0, segment_result.content_markdown.encode("utf-8"))]
    assert client.lookup_entity_files_calls == []
    assert client.put_edge_calls == []
    assert client.index_entity_calls == []


# ---------------------------------------------------------------------------
# entities -> ENTITY_COOCCUR + entity.idx
# ---------------------------------------------------------------------------


def test_entity_cooccurrence_creates_edge_to_other_file():
    client = _FakeWiringClient(next_file_id=42, entity_files={"Acme Corp": [7, 8]})
    segment_result = _segment(entities=["Acme Corp"])

    result = execute_segment(segment_result, client, resolve_topic_file_id=_no_resolver)

    assert client.lookup_entity_files_calls == ["Acme Corp"]
    assert set(client.put_edge_calls) == {
        (42, 7, ENTITY_COOCCUR, 1),
        (42, 8, ENTITY_COOCCUR, 1),
    }
    assert client.index_entity_calls == [("Acme Corp", 42)]
    assert result.entity_cooccur_edges_created == 2
    assert result.errors == ()


def test_entity_cooccurrence_skips_self_edge():
    client = _FakeWiringClient(next_file_id=42, entity_files={"Acme Corp": [42, 7]})
    segment_result = _segment(entities=["Acme Corp"])

    result = execute_segment(segment_result, client, resolve_topic_file_id=_no_resolver)

    assert client.put_edge_calls == [(42, 7, ENTITY_COOCCUR, 1)]
    assert result.entity_cooccur_edges_created == 1


def test_duplicate_entities_indexed_once():
    client = _FakeWiringClient(next_file_id=42, entity_files={"Acme Corp": []})
    segment_result = _segment(entities=["Acme Corp", "Acme Corp"])

    execute_segment(segment_result, client, resolve_topic_file_id=_no_resolver)

    assert client.lookup_entity_files_calls == ["Acme Corp"]
    assert client.index_entity_calls == [("Acme Corp", 42)]


def test_entity_edge_failure_collected_not_raised():
    client = _FakeWiringClient(
        next_file_id=42,
        entity_files={"Acme Corp": [7], "Globex": [9]},
        put_edge_errors={(42, 7, ENTITY_COOCCUR): RuntimeError("edge store down")},
    )
    segment_result = _segment(entities=["Acme Corp", "Globex"])

    result = execute_segment(segment_result, client, resolve_topic_file_id=_no_resolver)

    assert len(result.errors) == 1
    assert "Acme Corp" in result.errors[0]
    assert "edge store down" in result.errors[0]
    # The other entity's edge still gets created despite the first one failing.
    assert (42, 9, ENTITY_COOCCUR, 1) in client.put_edge_calls
    assert result.entity_cooccur_edges_created == 1
    # index_entity is still attempted for both entities (best-effort).
    assert ("Acme Corp", 42) in client.index_entity_calls
    assert ("Globex", 42) in client.index_entity_calls


# ---------------------------------------------------------------------------
# related_topics -> LLM_ASSERTED
# ---------------------------------------------------------------------------


def test_related_topics_create_llm_asserted_edges():
    client = _FakeWiringClient(next_file_id=42)
    segment_result = _segment(related_topics=["billing/Refunds", "billing/Chargebacks"])

    def resolver(path: str) -> int | None:
        return {"billing/Refunds": 10, "billing/Chargebacks": 11}.get(path)

    result = execute_segment(segment_result, client, resolve_topic_file_id=resolver)

    assert set(client.put_edge_calls) == {
        (42, 10, LLM_ASSERTED, 1),
        (42, 11, LLM_ASSERTED, 1),
    }
    assert result.llm_asserted_edges_created == 2
    assert result.errors == ()


def test_related_topic_self_edge_skipped():
    client = _FakeWiringClient(next_file_id=42)
    segment_result = _segment(related_topics=["self/topic"])

    result = execute_segment(
        segment_result, client, resolve_topic_file_id=lambda p: 42
    )

    assert client.put_edge_calls == []
    assert result.llm_asserted_edges_created == 0


def test_unresolvable_related_topic_collected_not_raised():
    client = _FakeWiringClient(next_file_id=42)
    segment_result = _segment(related_topics=["billing/Refunds", "unknown/topic"])

    def resolver(path: str) -> int | None:
        return {"billing/Refunds": 10}.get(path)

    result = execute_segment(segment_result, client, resolve_topic_file_id=resolver)

    assert client.put_edge_calls == [(42, 10, LLM_ASSERTED, 1)]
    assert result.llm_asserted_edges_created == 1
    assert len(result.errors) == 1
    assert "unknown/topic" in result.errors[0]


def test_duplicate_related_topics_single_edge():
    client = _FakeWiringClient(next_file_id=42)
    segment_result = _segment(related_topics=["billing/Refunds", "billing/Refunds"])

    result = execute_segment(
        segment_result, client, resolve_topic_file_id=lambda p: 10
    )

    assert client.put_edge_calls == [(42, 10, LLM_ASSERTED, 1)]
    assert result.llm_asserted_edges_created == 1


# ---------------------------------------------------------------------------
# Combined: full fixture segment set (entities + related_topics + CREATE_NEW)
# ---------------------------------------------------------------------------


def test_full_fixture_segment_set():
    client = _FakeWiringClient(
        next_file_id=100,
        entity_files={"Acme Corp": [1, 2], "Jane Doe": []},
    )
    segment_result = _segment(
        topic_action="CREATE_NEW",
        new_topic_path="billing/InvoiceDisputes",
        content_markdown="# Dispute\nAcme Corp disputes invoice #123, contact Jane Doe.",
        entities=["Acme Corp", "Jane Doe"],
        related_topics=["billing/PaymentTerms"],
    )

    def resolver(path: str) -> int | None:
        return {"billing/PaymentTerms": 55}.get(path)

    result = execute_segment(segment_result, client, resolve_topic_file_id=resolver)

    assert client.put_segment_calls == [(0, segment_result.content_markdown.encode("utf-8"))]
    assert set(client.put_edge_calls) == {
        (100, 1, ENTITY_COOCCUR, 1),
        (100, 2, ENTITY_COOCCUR, 1),
        (100, 55, LLM_ASSERTED, 1),
    }
    assert sorted(client.index_entity_calls) == [("Acme Corp", 100), ("Jane Doe", 100)]
    assert result.file_id == 100
    assert result.entity_cooccur_edges_created == 2
    assert result.llm_asserted_edges_created == 1
    assert result.errors == ()


# ---------------------------------------------------------------------------
# GrpcPutSegmentClient (real request/response translation, mocked channel/stub)
# ---------------------------------------------------------------------------


def test_grpc_put_segment_client_translates_request_response(monkeypatch):
    import sys

    fake_pb2 = MagicMock()
    fake_pb2_grpc = MagicMock()

    request_marker = object()
    fake_pb2.PutSegmentRequest.return_value = request_marker

    fake_response = MagicMock()
    fake_response.file_id = 42
    fake_response.new_version = 3

    fake_stub_instance = MagicMock()
    fake_stub_instance.PutSegment.return_value = fake_response
    fake_pb2_grpc.HiveMindStub.return_value = fake_stub_instance

    monkeypatch.setitem(sys.modules, "hivemind_pb2", fake_pb2)
    monkeypatch.setitem(sys.modules, "hivemind_pb2_grpc", fake_pb2_grpc)

    fake_channel = MagicMock()
    client = GrpcPutSegmentClient(fake_channel)
    result = client.put_segment(0, b"content bytes")

    fake_pb2_grpc.HiveMindStub.assert_called_once_with(fake_channel)
    fake_pb2.PutSegmentRequest.assert_called_once_with(file_id=0, content=b"content bytes")
    fake_stub_instance.PutSegment.assert_called_once_with(request_marker)
    assert result == PutSegmentResult(file_id=42, new_version=3)
