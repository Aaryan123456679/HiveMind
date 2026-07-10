---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `engine/rpc/`

Status: `.proto` contracts defined (`proto/hivemind.proto`, task-3.2.1, issue #16) with
generated Go (`engine/rpc/gen/`) and Python (`agents/hivemind_pb2*.py`) stubs checked in.
Server handler implementations for `PutSegment`/`GetFile`/`ReadPartial`/`GraphNeighbors`/
`SearchCandidates` (`engine/rpc/server.go`, task-3.2.2) and the real gRPC-backed
`ProposeSplit` *client* (`engine/split/proposer_grpc.go`, task-3.2.3) are implemented.
`ProposeSplit`'s *server* side remains the generated `Unimplemented` stub: the real
LLM-backed Python ingestion-agent service is out of scope for issue #16 (see issue #18).
Per-call latency/cost interceptor (task-3.2.4) and the cross-process integration test
(task-3.2.5) are implemented. `PutEdge`/`PutEntity`/`LookupEntity` were added later, as
user-authorized new scope discovered during issue #18 subtask 3.4.4's verification (not
one of issue #18's own numbered subtasks) -- see the "Exposed RPCs" section below and
`engine/rpc/server.go`'s doc comments for their handlers. See [HLD.md](../HLD.md) for
system context.

## Purpose

gRPC server exposing engine operations to the [API gateway](../HLD.md#3-architecture) (`api/`),
and — for split proposals — acting as a gRPC *client* of the Python agent service (see
[split.md](split.md)).

## Exposed RPCs

- `PutSegment` — write a segment produced by the ingestion segmentation agent into a topic file
  (append or create).
- `GetFile` — full-file read at the current MVCC snapshot.
- `ReadPartial` — section-level read using the markdown header-offset cache (see
  [catalog.md](catalog.md) staleness risk).
- `Split` — engine-internal split entry point invoked when a threshold crossing is detected (see
  [split.md](split.md)). **Not** part of `proto/hivemind.proto`'s gRPC surface: issue #16's
  task-3.2.1 acceptance criteria names exactly six RPCs (`PutSegment`, `GetFile`, `ReadPartial`,
  `GraphNeighbors`, `SearchCandidates`, `ProposeSplit`) and intentionally omits `Split`, which is
  invoked in-process within `engine/` rather than over the wire. This list stays 6-wide in the
  proto by design; if `Split` is ever exposed cross-process it should be added as an explicit,
  separately-scoped RPC in a later subtask, not folded silently into 3.2.2's server surface.
- `GraphNeighbors` — graph traversal, delegates to [graph.md](graph.md).
- `SearchCandidates` — non-LLM candidate topic search consumed by the Python
  [query-agent](query-agent.md)'s topic-selector.
- `PutEdge` — appends one occurrence of a graph edge (`ENTITY_COOCCUR`, `LLM_ASSERTED`,
  `SPLIT_SIBLING`, or `REDIRECT`) between two fileIDs to `engine/graph`'s per-node edge log
  (`EdgeLog.AppendEdge`). Weight-increment/dedup semantics across repeated occurrences are
  performed later, by `engine/graph.Compact` (already implemented, task-3.1.3) -- `PutEdge`
  itself does not sum weights. New scope, added during issue #18 subtask 3.4.4's
  verification (see [graph.md](graph.md)'s "Edge shape").
- `PutEntity` / `LookupEntity` — register/read the `entity.idx` association between an
  entity name and one or more fileIDs (see [ingestion-agent.md](ingestion-agent.md)),
  backed by a dedicated `engine/btree` tree keyed under a reserved
  `"\x00entity\x00<name>\x00<fileID>"` prefix (see `engine/rpc/server.go`'s `PutEntity`/
  `LookupEntity` doc comments for the exact key format and why a separate tree from the
  path index is used). New scope, added during issue #18 subtask 3.4.4's verification.

## Consumed RPC (client side)

- `ProposeSplit` on the Python ingestion service — called from `engine/split/` during auto-split
  (see [split.md](split.md) and [ingestion-agent.md](ingestion-agent.md)).

## Design notes

- gRPC (not REST) is used specifically so both sides can attach interceptors logging per-call
  latency and (Python-side) LLM cost, feeding the benchmark harness (see [eval.md](eval.md)).
- Contracts are defined in `proto/` (shared `.proto` files between `engine/`, `api/`, and
  `agents/`) — see `proto/hivemind.proto`.

## Interactions with other modules

- `api/` — the HTTP gateway is the primary client of this server for `/ingest /query /graph
  /files /admin` routes.
- `split/` — both a caller (via `Split`) and, transitively, a client of the agent's `ProposeSplit`.
- `graph/`, `catalog/`, `mvcc/` — backing implementations for `GraphNeighbors`, `GetFile`,
  `ReadPartial`, `SearchCandidates`.

## Known risks

- None unique to this module; inherits the section-index staleness risk from
  [catalog.md](catalog.md)/[split.md](split.md) via `ReadPartial`.

## Cross-references

- [HLD.md](../HLD.md)
- [split.md](split.md), [graph.md](graph.md), [catalog.md](catalog.md)
- [ingestion-agent.md](ingestion-agent.md) — `ProposeSplit` callee
- [query-agent.md](query-agent.md) — `SearchCandidates` / `GraphNeighbors` caller
