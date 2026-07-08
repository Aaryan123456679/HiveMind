---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `engine/graph/`

Status: scaffold only (`engine/graph/doc.go` placeholder). See [HLD.md](../HLD.md) for system
context.

## Purpose

Adjacency store for the topic knowledge graph that links files/topics together, and the traversal
API the query pipeline uses to expand a candidate topic set.

## Storage layout

- `graph.dat`: CSR-like compact adjacency arrays per source `fileID`, with periodic compaction.
- Writes are append-only per-node edge logs, avoiding the need to lock a shared adjacency array.

### `graph.dat` CSR format (subtask 3.1.1, `engine/graph/csr.go`)

`graph.dat` is a single whole-snapshot binary file, atomically rewritten (temp file + fsync +
rename, following `engine/catalog/content.go`'s `writeContentFile` convention) on every write —
not an incrementally-appended log like `edge_append.go`'s `EdgeAppender`. Layout: a 28-byte header
(`"GCS1"` magic, format version, node count, edge count, CRC32(IEEE) of the payload) followed by
three contiguous arrays — sorted source `fileID`s, a CSR offsets array (length nodeCount+1), and a
flat array of fixed-width edge records (`Target`, `Type`, `Weight`, `LastUpdated`). This is the
persistence primitive only; the per-node edge-log writer (3.1.2) and the compaction step that
merges the edge log into this array (3.1.3, including `ENTITY_COOCCUR` weight increments) are
separate, later subtasks. See `engine/graph/csr.go`'s package doc comment for the full byte
layout.

### Per-node edge log (subtask 3.1.2, `engine/graph/edgelog.go`)

`EdgeLog` is the durable landing zone for newly discovered edges of any type
(`ENTITY_COOCCUR` with weight, `LLM_ASSERTED`, and future split edges), organized as one
append-only log per source `fileID` rather than one shared array or log — this is what lets
concurrent writers touching different `fileID`s (e.g. two ingestion workers processing
different files at once) proceed without contending on a shared lock. On disk:
`<root>/<sourceFileID>/wal-<N>.log`, one such subdirectory per source `fileID` that has ever
had an edge appended, reusing `engine/wal`'s own segment-writer/rotation primitive (the same
low-level building block `edge_append.go`'s `EdgeAppender` already uses, but with one
`wal.Writer` instance per `fileID` instead of one shared writer). Each `fileID` gets its own
`wal.Writer`, and `wal.Writer` already guards its own append/rotation state with its own
internal mutex, so appends to different `fileID`s contend on different mutexes; appends to
the *same* `fileID` are correctly serialized by that `fileID`'s own writer (a single node's
log is still an ordered, single-writer-at-a-time append log). `EdgeLog`'s own map of
`fileID -> *wal.Writer` uses a `sync.RWMutex` with a double-checked-lock pattern so opening
new per-node writers doesn't bottleneck concurrent access to already-open ones. Log entries
reuse `csr.go`'s `CSREdge` type verbatim (`{Target, Type, Weight, LastUpdated}`), since 3.1.3's
compaction step needs to merge/increment `ENTITY_COOCCUR` weights from this log — the narrower
`Edge` shape `edge_append.go`'s `EdgeAppender` uses (no weight/timestamp) cannot represent
that. This is a distinct mechanism from `EdgeAppender`, which remains scoped to
`SPLIT_SIBLING`/`REDIRECT` edges written by `engine/split/execute.go` as part of the atomic
split-commit WAL transaction (task-2b.3.6); `edgelog.go` does not modify, replace, or route
through `edge_append.go`. Edge-type creation/validation support beyond rejecting the
`EdgeTypeInvalid` zero-value sentinel is subtask 3.1.4's job
(`engine/graph/edge.go`) — this writer only persists and reads back whatever `CSREdge` values
it is given.

## Edge shape

```
{ targetFileID, edgeType, weight, lastUpdated }
```

`edgeType` is one of:

- `ENTITY_COOCCUR` — incremented when the ingestion segmentation agent extracts co-occurring
  entities across files (see [ingestion-agent.md](ingestion-agent.md)).
- `LLM_ASSERTED` — created from the segmentation agent's `related_topics` output.
- `SPLIT_SIBLING` — created between files produced by the same [auto-split](split.md) event.
- `REDIRECT` — points from an old, split-away path to its redirect stub.

## Traversal API

`GraphNeighbors(fileID, depth, edgeTypeFilter, maxNodes)` — used by the engine to expand topics the
query-time topic-selector judges insufficient alone (0-2 hop traversal), and hard-capped
system-wide at `k + 2k` total files to prevent context blow-up (see
[query-agent.md](query-agent.md)).

## Interactions with other modules

- `split/` — adds `SPLIT_SIBLING` edges and retargets inbound edges to redirect stubs during a
  split.
- `ingestion-agent` (`agents/ingestion/`) — the source of `ENTITY_COOCCUR` weight increments and
  `LLM_ASSERTED` edges.
- `query-agent` (`agents/query/`) — the consumer of `GraphNeighbors` for graph-aware retrieval
  expansion.

## Known risks

- **Graph traversal context blow-up** — mitigated by the hard `k + 2k` file cap on `GraphNeighbors`
  expansion; the benchmark suite ([eval.md](eval.md)) must measure whether traversal ever hurts
  precision, not just recall.

## Cross-references

- [HLD.md](../HLD.md)
- [split.md](split.md) — edge creation during splits
- [ingestion-agent.md](ingestion-agent.md) — edge creation during ingestion
- [query-agent.md](query-agent.md) — traversal consumer
- [eval.md](eval.md) — benchmark measurement of traversal precision/recall tradeoffs
