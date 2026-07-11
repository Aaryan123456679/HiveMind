---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: Benchmark Harness (`agents/eval/` + `engine/loadtest/`)

Status: scaffold only (`agents/eval/__init__.py` empty; `engine/loadtest/doc.go` placeholder). See
[HLD.md](../HLD.md) for system context.

This doc covers both benchmark surfaces together since they answer the same overall question
("does HiveMind's storage + retrieval design hold up?") from two angles: retrieval quality
(`agents/eval/`) and storage-engine concurrency (`engine/loadtest/`).

## `agents/eval/` — retrieval quality benchmark

### Purpose

Compares HiveMind's retrieval quality, latency, and cost against baselines as the corpus grows.

### Dataset loaders

- Support tickets (e.g. Bitext)
- Enron email subsample
- Synthetic policy/manual PDFs, seeded with ~30-50 predefined ground-truth topics and deliberate
  cross-topic references (see [HLD.md](../HLD.md#2-stack-decisions)).

Ground-truth topic/query labels are attached to this dataset for recall/precision measurement.

### Retrieval arms

1. **HiveMind** — the full pipeline in [query-agent.md](query-agent.md) over the
   [engine](../HLD.md#31-go-storage-engine-engine)'s topic-file/graph store.
2. **Classic vector RAG** — fixed-size-chunk baseline. Per system-wide risk tracking, this baseline
   must be genuinely well-tuned (real chunk size/overlap, reranking if time allows) — not a
   strawman (see [HLD.md](../HLD.md#7-system-wide-known-risks)).
3. **Simplified GraphRAG-style** — entity-graph retrieval baseline.

All three arms share an identical final-answer LLM (via [llm-provider.md](llm-provider.md)) so that
only the retrieval step varies between arms. This is enforced in code, not just documented here:
`agents/eval/pipeline.py` (issue #27, subtask 5.2.4) exposes a single shared
`generate_final_answer()` function that every arm's runner (`run_hivemind_arm`,
`run_vector_rag_arm`, `run_graphrag_lite_arm`) calls for its final-answer step -- itself a thin
reuse of the real, already-implemented production call path,
[`query.synthesizer.synthesize_answer`](query-agent.md#synthesizerpy), not a second parallel
implementation. See `agents/eval/test_shared_final_llm.py` for the call-signature-identity
proof.

### Metrics

- Topic-level recall/precision@k
- LLM-judge answer quality + manual spot-check calibration
- Per-stage latency
- $/1000-query cost
- **Corpus-growth-checkpoint degradation chart** at 20% / 50% / 100% ingested — the key novelty
  result of the project (see [HLD.md](../HLD.md#1-project-identity)).

## `engine/loadtest/` — storage-engine concurrency benchmark

### Purpose

Custom load-generation harness (goroutines + `sync.WaitGroup` + a results-collecting channel +
histogram via `hdrhistogram`), used for:

- Concurrent ingestion throughput benchmarks (`testing.B`, with LLM calls mocked to isolate the
  storage engine itself).
- Concurrent query latency under concurrent ingestion load (p50/p95/p99) — the expectation is flat
  query latency thanks to [MVCC](mvcc.md).
- The auto-split race correctness test (see [split.md](split.md) — many goroutines appending to
  the same file simultaneously; asserts no data loss, exactly-one split per threshold crossing, no
  dangling graph edges).

All concurrency tests here are gated by `go test -race` (see [AGENT.md](../../AGENT.md)).

## Interactions with other modules

- `agents/query/`, `engine/rpc/`, `engine/graph/` — the HiveMind retrieval arm under test.
- `agents/llm/` — shared final-answer LLM and per-call cost/latency interceptor data source.
- `engine/mvcc/`, `engine/split/`, `engine/catalog/`, `engine/btree/` — the concurrency-critical
  paths `engine/loadtest/` exercises.

## Known risks

- **Benchmark fairness** — vector-RAG baseline must be well-tuned, not a strawman.
- **Graph traversal context blow-up** — the `agents/eval/` metrics must explicitly check whether
  graph expansion hurts precision at any corpus-growth checkpoint, not just whether it helps
  recall.
- **Auto-split correctness under concurrency** — surfaced here as the race test in
  `engine/loadtest/`; this is the highest-risk correctness surface in the engine (see
  [split.md](split.md)).

## Cross-references

- [HLD.md](../HLD.md)
- [query-agent.md](query-agent.md) — HiveMind retrieval arm
- [llm-provider.md](llm-provider.md) — shared final-answer LLM + cost/latency interceptors
- [split.md](split.md) — auto-split race test target
- [mvcc.md](mvcc.md) — flat-query-latency-under-write-load expectation
