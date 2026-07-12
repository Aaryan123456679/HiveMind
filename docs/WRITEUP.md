# HiveMind — Technical Write-up

This is a short technical write-up of HiveMind: what it is, how it's built, how its retrieval
approach differs from existing prior art, what the live benchmark run actually showed, and what
was learned (including the honest limitations) while building it. It intentionally summarizes
rather than duplicates [docs/HLD.md](HLD.md), [docs/LLD/](LLD/), and [README.md](../README.md);
see those for full detail and for the authoritative source of any number restated here.

## 1. System architecture

HiveMind is a self-organizing, file-based knowledge store intended as a RAG replacement. Instead
of chunking documents into a vector DB, an LLM parses each incoming document once at ingestion
time and segregates its information into topic-coherent `.md` files (not vector chunks). Files
auto-split when they grow too large, and topics are linked via a traversable metadata knowledge
graph. Retrieval at query time uses a two-agent pipeline (intent-refiner + graph-aware
topic-selector with a tunable `k`).

At the system level:

```
Client/UI (React) -> API Gateway (Go, api/) -> gRPC -> Go Storage Engine (engine/)
                                              -> gRPC -> Python ML/Agent Service (agents/) -> LLM Provider Layer (Ollama | OpenRouter | Gemini)
```

Go and Python communicate over gRPC (not REST) specifically so per-call latency and cost can be
logged via interceptors on both sides, feeding the benchmark harness. The call graph is
bidirectional: the API gateway calls into both the engine and the agent service for normal
traffic, and the engine's `split` module calls back into the agent service's `ProposeSplit` RPC
during auto-split.

**Go storage engine (`engine/`)** — the primary distributed-systems credibility piece of the
project, a custom on-disk storage engine (own catalog, indexing, locking), explicitly not
Postgres/SQLite:

| Module | Responsibility |
|---|---|
| `catalog/` | On-disk metadata catalog, slotted 4KB pages, striped-mutex concurrency |
| `btree/` | On-disk B+Tree mapping topic paths to fileIDs, latch-crabbing + optimistic reads |
| `mvcc/` | Per-file multi-version concurrency control, epoch-based GC |
| `split/` | Auto-split orchestration when a file exceeds its size threshold |
| `graph/` | Adjacency store + traversal API over topic/file relationships |
| `wal/` | Write-ahead log + checkpointing + crash recovery |
| `rpc/` | gRPC server exposing engine operations, and client of the agent's `ProposeSplit` |
| `loadtest/` | Concurrency/load-generation harness for benchmarking |

`api/` is a separate Go module: the HTTP gateway providing auth, rate limiting, and routes
(`/ingest /query /graph /files /admin`) that fan out to the engine and agent service via gRPC.

**Python ML/agent layer (`agents/`)**:

| Module | Responsibility |
|---|---|
| `ingestion/` | Per-doc-type normalizers + segmentation agent + `ProposeSplit` |
| `query/` | Intent-refiner, topic-selector, synthesizer |
| `llm/` | Provider-agnostic LLM client interface (Ollama / OpenRouter / Gemini) |
| `eval/` | Benchmark harness against vector-RAG and GraphRAG baselines |

The core storage-model bet is: an MVCC- and auto-split-managed, graph-linked `.md` file store
that enforces topical coherence at the storage layer, in place of the usual vector-chunk
index used by conventional RAG.

## 2. Novelty vs. prior art

Per [HLD.md section 6](HLD.md#6-why-this-exists-novelty-vs-prior-art):

- **GraphRAG** builds graphs over entities extracted from chunks, but retrieval still returns
  chunk/summary text — there is no living, self-splitting document store underneath it.
- **RAPTOR** does static hierarchical clustering at index-build time. There's no incremental
  reorganization, and its tree nodes are LLM summaries, not original content.
- **MemGPT/Letta** page hierarchical memory for a single agent's context window — not a shared,
  graph-linked, browsable multi-user corpus.
- **HiveMind's contribution**: the storage engine itself enforces topical coherence and
  consistency (via MVCC and auto-split) under concurrent access, combined with a learned/agentic
  graph-aware retrieval step that replaces embedding similarity search — explicitly evaluated
  against corpus-growth degradation (measured at 20% / 50% / 100% of the corpus ingested).

These claims are restated here as-is from the HLD; no additional or stronger claims are made in
this write-up.

## 3. Benchmark results

`results/run-001/` is the first full live paid-API benchmark run: OpenRouter `gpt-4o-mini` for
both the query LLM and the judge, comparing HiveMind against a plain vector-RAG baseline and a
lightweight GraphRAG-style baseline (`graphrag_lite`), at 20% / 50% / 100% checkpoints of a
32-document corpus, 32 queries per checkpoint, LLM-judged answer quality on a 1-5 scale. Actual
spend was $0.1677 against a $2.00 cost cap; wall clock was approximately 35 minutes.

| checkpoint | arm | recall | precision | judge score | cost / 1k queries |
|---|---|---|---|---|---|
| 20% | hivemind | 0.062 | 0.188 | 3.59 | $0.089 |
| 20% | vector_rag | 0.219 | 0.131 | 3.72 | $0.094 |
| 20% | graphrag_lite | 0.188 | 0.113 | 3.58 | $0.093 |
| 50% | hivemind | 0.146 | 0.375 | 3.77 | $0.089 |
| 50% | vector_rag | 0.458 | 0.275 | 3.96 | $0.096 |
| 50% | graphrag_lite | 0.198 | 0.119 | 3.64 | $0.092 |
| 100% | hivemind | 0.302 | 0.719 | 4.23 | $0.094 |
| 100% | vector_rag | 0.823 | 0.494 | 4.42 | $0.096 |
| 100% | graphrag_lite | 0.260 | 0.156 | 3.68 | $0.092 |

(Numbers reproduced from `results/run-001/live_benchmark_results.json`; full data, an ASCII chart,
and an HTML report are also in `results/run-001/` as `chart.txt` and `report.html`, and run
metadata — model, cost cap, actual spend — is in `run-metadata.json`.)

Honest read of this run: on this small (32-doc) corpus, plain vector RAG currently leads on raw
recall and judge score at every checkpoint. HiveMind's precision improves faster with corpus
growth than either baseline's (0.19 -> 0.38 -> 0.72), and per-query cost stays flat and comparable
across all three arms — but this is a single run on a small corpus, not a claim of overall
superiority. Results will vary run to run; rerun with `python -m eval.run_live_benchmark` for a
fresh sample.

## 4. Lessons learned / known limitations

- **Load-test wall-clock is fsync-bound, not concurrency-bound.** The auto-split race-at-scale
  test (`engine/loadtest/split_race_scale_test.go`) found that wall-clock time in that scenario
  is dominated by total append COUNT rather than goroutine/worker count, because every append
  against the single currently-active fileID fully serializes on a fsync'd WAL record
  (`catalog.ContentStore.Append`). The original 2,400-append run measured roughly 54ms/append; a
  naive worker-count-only scale-up would have made wall clock balloon to 40+ minutes. The test
  was instead scaled by increasing worker count sharply (40 -> 200, a 5x increase, to stress real
  lock contention) while holding total append volume to only ~2x (2,400 -> 4,800), keeping wall
  clock bounded under the same fsync-dominated cost model. This is a concrete example of a
  storage-engine design constraint (single-writer-per-active-file serialization on durable
  append) directly shaping how its own tests must be scaled.
- **Small-corpus benchmark caveat.** The only live benchmark run to date is a single pass over a
  32-document corpus with 32 queries per checkpoint. The headline result (vector RAG leading on
  recall/judge score, HiveMind's precision improving faster with corpus growth) should be read as
  a first data point, not a settled conclusion — a larger corpus and repeated runs are needed
  before drawing stronger claims either way.
- **OCI live deployment was never actually verified.** `deploy/oci/` documents/automates
  provisioning HiveMind on an Oracle Cloud Always-Free ARM instance via k3s, but its status is
  explicitly "unverified against a live OCI account" — `provision.sh` has been reviewed and
  `bash -n` syntax-checked only, since no OCI credentials existed in the sandbox it was written
  in. Anyone using it should read it fully and dry-run/review before trusting it against a real
  account.
- **Catalog free-list has a hard page-count ceiling.** The on-disk catalog's bitmap-based
  free-list caps out at 32,704 allocatable pages (`(4096-8)*8`), i.e. a maximum `catalog.dat` size
  of roughly 128MB. `AllocatePage` fails with an explicit "free-list exhausted" error rather than
  corrupting state once this ceiling is hit — a known, accepted limitation for this build phase,
  pending a future extensible/multi-page free-list representation.
- **Query-agent prefix-only candidate search has a disclosed residual gap.** Multi-word candidate
  search merges one `btree.PrefixScan` per query term, which requires at least one term to be a
  leading path-segment token in the index. If a query consists entirely of terms that are not
  themselves leading path segments (e.g. all stopwords), the merged candidate pool stays empty.
  Closing this would require a genuinely non-prefix index primitive or a different index
  structure, which was explicitly scoped out and left as a disclosed, non-blocking limitation.

## Sources

- [docs/HLD.md](HLD.md) — architecture (section 3) and novelty vs. prior art (section 6).
- [docs/LLD/](LLD/) — per-module design detail, including `catalog.md`, `query-agent.md`,
  `split.md`.
- [README.md](../README.md) — "Benchmark results" and "Load testing" sections.
- `results/run-001/live_benchmark_results.json`, `run-metadata.json`, `chart.txt` — raw benchmark
  data underlying section 3 above.
- `engine/loadtest/split_race_scale_test.go` — package doc comment describing the fsync-bound
  scaling rationale.
- `deploy/oci/README.md` — OCI deploy path status.
