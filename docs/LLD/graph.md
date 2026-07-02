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
