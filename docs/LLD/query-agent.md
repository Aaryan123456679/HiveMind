---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `agents/query/`

Status: scaffold only (`agents/query/__init__.py` empty). See [HLD.md](../HLD.md) for system
context.

## Purpose

Query-time retrieval pipeline: refine intent, select and expand relevant topics via the graph, and
synthesize a final answer with citations. Three components, each a hosted-small-model agent (see
[llm-provider.md](llm-provider.md)).

## `intent_refiner.py`

Input: raw query + short history.
Output: `{ refined_intent, entities: [], query_type }`.

## `topic_selector.py`

- Receives a candidate topic list from a non-LLM Go-side `SearchCandidates` call (see
  [rpc.md](rpc.md)).
- Selects the top-`k` topics, where `k` is a tunable hyperparameter (default 3).
- For any topic it judges insufficient alone, it may also request graph-traversal expansion
  (0-2 hops); the Go engine performs the expansion via `GraphNeighbors` (see [graph.md](graph.md)).
- The combined result is **hard-capped at `k + 2k` total files** to prevent context blow-up — this
  is a system-wide invariant, not just an implementation detail (see
  [HLD.md](../HLD.md#7-system-wide-known-risks)).

## `synthesizer.py`

Final LLM call: refined intent + concatenated selected markdown (with file-path headers) -> answer
with inline file-path citations.

## Pipeline order

```
query -> intent_refiner -> topic_selector (+ SearchCandidates / GraphNeighbors) -> synthesizer -> answer
```

## Interactions with other modules

- `engine/rpc/` — `SearchCandidates` (candidate generation) and `GraphNeighbors` (graph
  expansion) are both engine RPCs consumed here.
- `engine/graph/` — traversal semantics and edge-type filtering used by `GraphNeighbors`.
- `agents/llm/` — provider abstraction for all three hosted-model calls in this pipeline.
- `agents/eval/` — the query pipeline is one of the arms benchmarked (HiveMind arm vs. vector-RAG
  vs. GraphRAG-style baseline).

## Known risks

- **Graph traversal context blow-up** — mitigated by the `k + 2k` hard cap; the benchmark suite
  must measure whether traversal ever *hurts* precision (not just whether it helps recall). See
  [eval.md](eval.md).

## Cross-references

- [HLD.md](../HLD.md)
- [rpc.md](rpc.md) — `SearchCandidates` / `GraphNeighbors` RPCs
- [graph.md](graph.md) — traversal implementation
- [llm-provider.md](llm-provider.md) — LLM client abstraction
- [eval.md](eval.md) — benchmark arm using this pipeline
