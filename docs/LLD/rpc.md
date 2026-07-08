---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `engine/rpc/`

Status: `.proto` contracts defined (`proto/hivemind.proto`, task-3.2.1, issue #16) with
generated Go (`engine/rpc/gen/`) and Python (`agents/hivemind_pb2*.py`) stubs checked in.
Server handler implementations (`engine/rpc/server.go`) and the real gRPC-backed
`ProposeSplit` client (`engine/split/proposer_grpc.go`) are not yet implemented (task-3.2.2/
3.2.3). See [HLD.md](../HLD.md) for system context.

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
