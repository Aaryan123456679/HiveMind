"""Wiring: execute a parsed segment via `PutSegment`, then wire `entities` into
`ENTITY_COOCCUR` graph edges and `related_topics` into `LLM_ASSERTED` graph edges.

Per issue #18 subtask 3.4.4 and `docs/LLD/ingestion-agent.md`'s "What the Go engine
does with each segment" section:

```
- Executes the append/create via PutSegment.
- `entities` feed `entity.idx` and increment `ENTITY_COOCCUR` edge weights.
- `related_topics` become `LLM_ASSERTED` edges in the same graph.
```

This module consumes a :class:`~ingestion.segment.SegmentResult` (3.4.3) and drives
those three effects through an injected client.

Real vs. Protocol-only RPC surface -- disclosed, load-bearing choice
---------------------------------------------------------------------
`PutSegment` is a real RPC: `proto/hivemind.proto` defines
`PutSegmentRequest{file_id, content}` / `PutSegmentResponse{file_id, new_version}`, and
`engine/rpc/server.go` implements it (task-3.2.2, already verified+committed). This
module's `GrpcPutSegmentClient` wraps it for real, following `ingestion.shortlist`
3.4.2's `GrpcSearchCandidatesClient` pattern exactly (lazy stub import with a `sys.path`
fallback, so importing this module never requires `grpc` to be installed or an engine to
exist).

`entity.idx` and any graph edge-write operation, however, have **no real RPC anywhere**
in the current system:

- `proto/hivemind.proto` defines exactly six RPCs (`PutSegment`, `GetFile`,
  `ReadPartial`, `GraphNeighbors`, `SearchCandidates`, `ProposeSplit`) --
  `docs/LLD/rpc.md` states this list is frozen by issue #16's task-3.2.1 acceptance
  criteria, and any new RPC needs its own separately-scoped subtask. `GraphNeighbors`
  is read-only traversal; there is no `AddEdge`/`PutEdge`/similar write-path RPC.
- The only Go-side edge-write primitives (`engine/graph/edgelog.go`'s
  `EdgeLog.AppendEdge`, `engine/graph/edge_append.go`'s `EdgeAppender.AppendEdge`) are
  engine-internal Go types with no gRPC handler wrapping them.
- `entity.idx` itself does not exist as a real index/store anywhere: searching
  `engine/` and `agents/` for `entity.idx` / `EntityIndex` finds nothing outside this
  LLD's own prose (`docs/LLD/ingestion-agent.md`, `docs/LLD/graph.md`).

Because of this, `SegmentWiringClient` below is a *Protocol*: `put_segment` is backed
by a real client (`GrpcPutSegmentClient`); `lookup_entity_files`, `index_entity`, and
`put_edge` have **no concrete gRPC-backed implementation shipped in this commit** --
only the Protocol shape, satisfied by test doubles (per the issue's own test spec:
"engine RPC client mocked"). Adding real RPCs for entity-index lookups and graph edge
writes is necessarily a separate, proto-touching subtask (outside this subtask's
`agents/ingestion/wiring.py`-only impacted-module scope) -- flagged forward, not
silently worked around.

`ENTITY_COOCCUR` semantics -- disclosed, deliberate deviation from a literal reading
-----------------------------------------------------------------------------------------
The issue's own design guidance suggests "increment ENTITY_COOCCUR edge weights between
co-occurring entities (pairwise, within the same segment)". That reading is not
representable in the current graph model: `docs/LLD/graph.md`'s "Edge shape" section
defines every edge as `{ targetFileID, edgeType, weight, lastUpdated }` -- edge
endpoints are always fileIDs, never entity strings; there is no entity-as-graph-node
concept anywhere in `engine/graph/`. `docs/LLD/graph.md` line 105 is the more specific,
authoritative semantic description: "`ENTITY_COOCCUR` -- incremented when the ingestion
segmentation agent extracts co-occurring entities **across files**." This module
therefore implements *that* reading: for each entity a segment mentions, look up which
*other files* already mention that same entity (via `entity.idx`, i.e.
`lookup_entity_files`), and create/increment an `ENTITY_COOCCUR` edge between this
segment's file and each such other file -- then register this file under the entity for
future segments' lookups (`index_entity`). This is real cross-file co-occurrence
tracking, consistent with the graph's actual (fileID-only) edge model.

`new_topic_path` cannot be registered anywhere queryable today -- disclosed, out-of-scope gap
-------------------------------------------------------------------------------------------------
`PutSegmentRequest` carries only `file_id` (uint64) + `content` (bytes) -- no path
field at all. `engine/rpc/server.go`'s already-implemented `PutSegment` CREATE path
never sets `catalog.CatalogRecord.PathHash` (the only path-shaped field in the entire
catalog record) from any request field, and no btree-insert call is wired from
`PutSegment` either. In other words, a file created via `PutSegment(file_id=0, ...)`
today is **not discoverable by path** via `SearchCandidates` afterwards -- this is a
real, pre-existing gap in already-committed engine code (task-3.2.2), not something in
this subtask's impacted-module scope (`agents/ingestion/wiring.py` only, no Go/proto
changes) to fix. `execute_segment` below calls `PutSegment` exactly per its real,
current contract and does not invent a client-side workaround that pretends path
registration works; flagged forward instead (see this run's handoff).

`target_topic` resolution -- disclosed choice, closes 3.4.3's forwarded F2
-------------------------------------------------------------------------------
`segment.py` (3.4.3) deliberately does not validate `target_topic` against the real
catalog, because `shortlist()`'s own pool is a bounded subset, not an exhaustive
membership list (see `segment.py`'s module docstring, "Cross-field validation
strictness"). 3.4.3's own commit doc explicitly forwards this as F2: "the correct
enforcement point is 3.4.4 ... which has access to the real catalog." This module closes
that gap: `execute_segment` takes a caller-supplied `resolve_topic_file_id` callable
(so this module itself does not need to make a `SearchCandidates` call --
keeping its only RPC dependency the one the issue names) and raises
`TopicNotFoundError` -- *before* any RPC call -- if an `APPEND_EXISTING` segment's
`target_topic` cannot be resolved to a real fileID.

Error-handling strategy -- disclosed, fail-fast vs. best-effort
---------------------------------------------------------------------
- **Fail-fast** for anything before/including the `PutSegment` call itself: an
  unresolvable `target_topic` (`TopicNotFoundError`) or a `put_segment` RPC failure
  (propagated unwrapped, mirroring `segment.py`'s "transport/provider failure
  propagates unwrapped" convention) both abort `execute_segment` immediately. Nothing
  else has happened yet at that point, so there is no partial state to reconcile.
- **Best-effort with error collection** for everything *after* a successful
  `PutSegment` call (the content write is already durable): each entity's co-occurrence
  lookup/edge-creation/indexing and each related-topic's resolution/edge-creation is
  attempted independently, catching and collecting any exception into the returned
  `SegmentExecutionResult.errors` tuple rather than raising. Losing one co-occurrence or
  `LLM_ASSERTED` edge must never silently roll back or hide the (expensive, already
  paid-for) successful content write, and must never be silently dropped either --
  every failure is collected with enough context (which entity/topic, which operation)
  for a caller to log, retry, or surface to an operator.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Callable, Protocol, Sequence

if TYPE_CHECKING:
    import grpc

    from ingestion.segment import SegmentResult

#: Resolves a topic path (e.g. `SegmentResult.target_topic` or one of
#: `SegmentResult.related_topics`) to a real fileID, or `None` if the path is not
#: (yet) known to the catalog. Caller-supplied -- e.g. backed by the shortlist already
#: fetched for this document (3.4.2) or a fresh `SearchCandidates` call -- so this
#: module's own only RPC dependency stays the one the issue names (`PutSegment`).
TopicResolverFn = Callable[[str], "int | None"]

#: The two edge types this module creates. Mirrors `proto/hivemind.proto`'s `EdgeType`
#: enum's canonical wire names exactly (see `docs/LLD/graph.md`'s "Edge shape" section).
ENTITY_COOCCUR: str = "ENTITY_COOCCUR"
LLM_ASSERTED: str = "LLM_ASSERTED"


class WiringError(Exception):
    """Base exception for this module's own (non-RPC-transport) failures."""


class TopicNotFoundError(WiringError):
    """Raised when an `APPEND_EXISTING` segment's `target_topic` does not resolve to a
    real fileID via the caller-supplied `resolve_topic_file_id`.

    Raised *before* any RPC call (fail-fast) -- see module docstring's
    "Error-handling strategy" section. Closes 3.4.3's forwarded finding F2.
    """


@dataclass(frozen=True)
class PutSegmentResult:
    """Mirrors `proto/hivemind.proto`'s `PutSegmentResponse` field-for-field."""

    file_id: int
    new_version: int


@dataclass(frozen=True)
class SegmentExecutionResult:
    """Outcome of `execute_segment`.

    Attributes:
        file_id: The fileID the segment's content was written to (returned by
            `PutSegment` -- newly allocated for `CREATE_NEW`, the resolved
            `target_topic` fileID for `APPEND_EXISTING`).
        new_version: The file's MVCC version after this write, as returned by
            `PutSegment`.
        entity_cooccur_edges_created: Count of `ENTITY_COOCCUR` edges successfully
            created/incremented (across all of this segment's `entities`).
        llm_asserted_edges_created: Count of `LLM_ASSERTED` edges successfully
            created (across all of this segment's `related_topics`).
        errors: Every best-effort-phase failure, as a descriptive string identifying
            which entity/topic and which operation failed. Empty iff every
            post-`PutSegment` operation succeeded. Never silently dropped -- see
            module docstring's "Error-handling strategy" section.
    """

    file_id: int
    new_version: int
    entity_cooccur_edges_created: int
    llm_asserted_edges_created: int
    errors: tuple[str, ...]


class SegmentWiringClient(Protocol):
    """The client surface `execute_segment` needs.

    `put_segment` is backed by a real RPC (see `GrpcPutSegmentClient`).
    `lookup_entity_files`/`index_entity`/`put_edge` have no real gRPC-backed
    implementation anywhere in this commit -- see module docstring's "Real vs.
    Protocol-only RPC surface" section for why. Tests supply a plain fake satisfying
    this Protocol structurally (no `grpc`/generated stubs required).
    """

    def put_segment(self, file_id: int, content: bytes) -> PutSegmentResult:
        """Execute a segment's content write. `file_id=0` means create; non-zero means
        append. Mirrors `proto/hivemind.proto`'s real `PutSegment` RPC exactly."""
        ...

    def lookup_entity_files(self, entity: str) -> Sequence[int]:
        """Return the fileIDs already indexed under `entity` in `entity.idx` (i.e.
        files whose segments previously mentioned this entity)."""
        ...

    def index_entity(self, entity: str, file_id: int) -> None:
        """Register `file_id` under `entity` in `entity.idx`, so future segments that
        mention the same entity can find it via `lookup_entity_files`."""
        ...

    def put_edge(
        self, source_file_id: int, target_file_id: int, edge_type: str, *, weight_delta: int = 1
    ) -> None:
        """Create (or increment the weight of) a `source_file_id -> target_file_id`
        edge of type `edge_type` (`ENTITY_COOCCUR` or `LLM_ASSERTED`)."""
        ...


def execute_segment(
    segment_result: "SegmentResult",
    rpc_client: SegmentWiringClient,
    *,
    resolve_topic_file_id: TopicResolverFn,
) -> SegmentExecutionResult:
    """Execute `segment_result` via `PutSegment`, then wire its `entities` into
    `ENTITY_COOCCUR` edges and its `related_topics` into `LLM_ASSERTED` edges.

    Args:
        segment_result: A validated segment, e.g. from `ingestion.segment.segment()`.
        rpc_client: Satisfies `SegmentWiringClient`. Tests mock this entirely (per the
            issue's test spec); `GrpcPutSegmentClient` is the real implementation of
            just the `put_segment` method (see module docstring for why the other
            three methods have no real implementation to offer yet).
        resolve_topic_file_id: Resolves a topic path to a fileID (or `None` if
            unknown). Used both for `target_topic` (required, fail-fast if
            unresolvable) and each of `related_topics` (best-effort, skipped with a
            collected error if unresolvable).

    Returns:
        A `SegmentExecutionResult` describing the write and every edge operation's
        outcome (see its docstring). `errors` is non-empty iff at least one
        post-`PutSegment` operation failed -- the content write itself always
        succeeded if this function returns at all (see Raises below).

    Raises:
        TopicNotFoundError: If `segment_result.topic_action == "APPEND_EXISTING"` and
            `resolve_topic_file_id(segment_result.target_topic)` returns `None`.
            Raised before any RPC call.
        Exception: Whatever `rpc_client.put_segment()` itself raises, propagated
            unwrapped (transport/provider failure, not this module's own concern to
            wrap -- mirrors `ingestion.segment`'s convention for its own upstream
            call). Nothing else is attempted if this happens.
    """
    if segment_result.topic_action == "APPEND_EXISTING":
        file_id_arg = resolve_topic_file_id(segment_result.target_topic)
        if file_id_arg is None:
            raise TopicNotFoundError(
                f"wiring: target_topic {segment_result.target_topic!r} "
                "(topic_action=APPEND_EXISTING) does not resolve to a known fileID"
            )
    else:
        file_id_arg = 0

    put_result = rpc_client.put_segment(
        file_id_arg, segment_result.content_markdown.encode("utf-8")
    )
    file_id = put_result.file_id

    errors: list[str] = []
    entity_edges_created = 0
    llm_asserted_edges_created = 0

    for entity in dict.fromkeys(segment_result.entities):
        try:
            other_file_ids = rpc_client.lookup_entity_files(entity)
        except Exception as exc:  # noqa: BLE001 -- best-effort phase, see module docstring
            errors.append(f"wiring: lookup_entity_files({entity!r}) failed: {exc}")
            other_file_ids = ()

        for other_file_id in other_file_ids:
            if other_file_id == file_id:
                continue
            try:
                rpc_client.put_edge(file_id, other_file_id, ENTITY_COOCCUR, weight_delta=1)
                entity_edges_created += 1
            except Exception as exc:  # noqa: BLE001
                errors.append(
                    f"wiring: put_edge(ENTITY_COOCCUR, {file_id} -> {other_file_id}, "
                    f"entity={entity!r}) failed: {exc}"
                )

        try:
            rpc_client.index_entity(entity, file_id)
        except Exception as exc:  # noqa: BLE001
            errors.append(f"wiring: index_entity({entity!r}, {file_id}) failed: {exc}")

    for related_topic in dict.fromkeys(segment_result.related_topics):
        target_file_id = resolve_topic_file_id(related_topic)
        if target_file_id is None:
            errors.append(
                f"wiring: related_topic {related_topic!r} does not resolve to a "
                "known fileID; LLM_ASSERTED edge skipped"
            )
            continue
        if target_file_id == file_id:
            continue
        try:
            rpc_client.put_edge(file_id, target_file_id, LLM_ASSERTED, weight_delta=1)
            llm_asserted_edges_created += 1
        except Exception as exc:  # noqa: BLE001
            errors.append(
                f"wiring: put_edge(LLM_ASSERTED, {file_id} -> {target_file_id}, "
                f"related_topic={related_topic!r}) failed: {exc}"
            )

    return SegmentExecutionResult(
        file_id=file_id,
        new_version=put_result.new_version,
        entity_cooccur_edges_created=entity_edges_created,
        llm_asserted_edges_created=llm_asserted_edges_created,
        errors=tuple(errors),
    )


def _import_hivemind_grpc_modules():
    """Import and return `(hivemind_pb2, hivemind_pb2_grpc)`, falling back to adding
    `agents/`'s absolute path onto `sys.path` if the plain top-level import fails.

    Duplicated from `ingestion.shortlist` deliberately (not imported from there): that
    module's own docstring frames this as a self-contained fallback tied to how the
    generated stubs are laid out (flat modules directly in `agents/`, not part of any
    installed package), not `shortlist`-specific behavior -- any module in
    `agents/ingestion/` that needs the generated stubs needs the same fallback, and
    importing it from `shortlist` would create a needless coupling between two
    otherwise-independent RPC client wrappers.
    """
    try:
        import hivemind_pb2
        import hivemind_pb2_grpc
    except ImportError:
        import sys
        from pathlib import Path

        agents_dir = str(Path(__file__).resolve().parent.parent)
        if agents_dir not in sys.path:
            sys.path.insert(0, agents_dir)
        import hivemind_pb2
        import hivemind_pb2_grpc
    return hivemind_pb2, hivemind_pb2_grpc


class GrpcPutSegmentClient:
    """Real `PutSegment` client: wraps `hivemind_pb2_grpc.HiveMindStub.PutSegment` over
    a caller-supplied `grpc.Channel`. Only implements `put_segment` -- see this
    module's docstring for why `lookup_entity_files`/`index_entity`/`put_edge` have no
    real implementation to offer (no such RPC exists yet). Not itself a full
    `SegmentWiringClient` -- combine with a caller-supplied entity/edge implementation
    (or a test double) to get one; there is currently no real one to combine with.

    Follows `ingestion.shortlist.GrpcSearchCandidatesClient`'s exact pattern: `grpc`/the
    generated stubs are imported lazily (not at module import time), so importing this
    class never requires `grpc` to be importable or an engine instance to exist.
    """

    def __init__(self, channel: "grpc.Channel") -> None:
        _, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

        self._stub = hivemind_pb2_grpc.HiveMindStub(channel)

    def put_segment(self, file_id: int, content: bytes) -> PutSegmentResult:
        hivemind_pb2, _ = _import_hivemind_grpc_modules()

        request = hivemind_pb2.PutSegmentRequest(file_id=file_id, content=content)
        response = self._stub.PutSegment(request)
        return PutSegmentResult(
            file_id=response.file_id, new_version=response.new_version
        )
