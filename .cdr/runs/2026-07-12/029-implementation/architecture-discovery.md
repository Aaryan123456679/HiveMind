# Architecture discovery

## Sources read (docs/index first, no raw source reading needed for a docs-only subtask)
- `docs/HLD.md` — full document read.
  - Section 3 (Architecture): Client/UI (React) -> API Gateway (Go, `api/`) -> gRPC -> Go
    Storage Engine (`engine/`); Go API also -> gRPC -> Python ML/Agent Service (`agents/`) ->
    LLM Provider Layer (Ollama | OpenRouter | Gemini). Go/Python communicate over gRPC (not
    REST) specifically so per-call latency/cost can be logged via interceptors for the
    benchmark. Bidirectional call graph: engine's `split` module calls back into agent
    service's `ProposeSplit` RPC during auto-split.
  - Section 3.1 (Go Storage Engine): `catalog/` (on-disk metadata, slotted 4KB pages,
    striped-mutex), `btree/` (B+Tree topic path -> fileID, latch-crabbing + optimistic
    reads), `mvcc/` (per-file MVCC, epoch-based GC), `split/` (auto-split orchestration),
    `graph/` (adjacency store + traversal), `wal/` (write-ahead log + checkpoint + crash
    recovery), `rpc/` (gRPC server), `loadtest/` (concurrency/load harness).
  - Section 3.2 (Python agent layer): `ingestion/` (normalizers + segmentation agent +
    ProposeSplit), `query/` (intent-refiner, topic-selector, synthesizer), `llm/`
    (provider-agnostic client: Ollama/OpenRouter/Gemini), `eval/` (benchmark harness).
  - Section 6 (novelty vs. prior art): GraphRAG (graphs over chunk-extracted entities, but
    retrieval still returns chunk/summary text, no living self-splitting store underneath),
    RAPTOR (static hierarchical clustering at index-build time, no incremental
    reorganization, tree nodes are LLM summaries not original content), MemGPT/Letta
    (hierarchical memory for a single agent's context window, not a shared graph-linked
    multi-user corpus). HiveMind's contribution: storage engine itself enforces topical
    coherence/consistency (MVCC + auto-split) under concurrent access, combined with a
    learned/agentic graph-aware retrieval step replacing embedding similarity search,
    evaluated explicitly against corpus-growth degradation (20/50/100% checkpoints).
  - Section 7 (known risks): LLM topic-boundary nondeterminism, auto-split correctness
    under concurrency (highest-risk correctness surface), graph traversal context blow-up
    (bounded by k + 2k file cap), benchmark fairness (vector-RAG baseline must be
    well-tuned), section-index staleness (markdown header-offset cache must be invalidated
    atomically with append/split transaction).

- `docs/LLD/catalog.md` — free-list/bitmap capacity ceiling: ~32,704 allocatable pages
  (~128MB catalog.dat max) is a known, accepted limitation for this phase (issue #51,
  subtask 4.5.13.2), pending a future extensible free-list representation.
- `docs/LLD/query-agent.md` — prefix-only candidate search residual limitation: if none of
  a query's whitespace-separated terms is itself a leading path-segment token, the merged
  candidate pool from `PrefixScan` stays empty; disclosed, non-blocking limitation (closing
  it needs a non-prefix index primitive, out of scope).
- `docs/LLD/split.md` — auto-split lease-based reclaim design has an accepted residual
  limitation around lease bookkeeping (`leaseEntry{deadline, gen, reclaimed}`).
- `engine/loadtest/split_race_scale_test.go` (package doc comment, read only the comment
  block, not full source) — key engineering insight: at load-test scale, wall-clock time in
  the auto-split race test is dominated by total append COUNT, not goroutine/worker count,
  because every append against the single currently-active fileID fully serializes on a
  fsync'd WAL record (`catalog.ContentStore.Append`). Measured ~54ms/append in the original
  2,400-append run; scaling worker count 5x (40->200) while holding total appends to ~2x
  (2,400->4,800) was the deliberate scale strategy to stress real concurrency without wall
  clock blowing up under the fsync-bound cost model.
- `deploy/oci/README.md` — explicit status line: "unverified against a live OCI account."
  `provision.sh` reviewed/`bash -n` syntax-checked only, never executed end-to-end against a
  real OCI account (no credentials in the sandbox it was written in).

- `README.md` sections "Benchmark results" and "Load testing" (already written, accurate,
  used verbatim as the numeric source together with the raw JSON):
  - Benchmark table (20/50/100% checkpoints x hivemind/vector_rag/graphrag_lite): recall,
    precision, judge score (1-5), cost/1k queries.
  - Honest-read paragraph: vector RAG leads on raw recall and judge score at every
    checkpoint on this 32-doc corpus; HiveMind's precision improves faster with corpus
    growth (0.19 -> 0.38 -> 0.72... actually values are 0.188/0.375/0.719, README rounds to
    0.19/0.38/0.72); cost/query flat and comparable across arms; single run on a small
    corpus, not a claim of overall superiority; results will vary run to run.
  - Load testing: `engine/loadtest/` harness (`harness.go`) + 3 tests
    (`BenchmarkIngestionThroughput`, `TestQueryLatencyUnderLoad`, `TestAutoSplitRaceAtScale`),
    all exercise the real storage engine (only LLM/segmentation boundary mocked), pass
    `-race` clean.

- `results/run-001/run-metadata.json` — run_id run-001, date 2026-07-12, OpenRouter
  gpt-4o-mini for both LLM and judge, cost cap $2.00, actual spend $0.1677, 32-doc corpus,
  32 queries/checkpoint, checkpoints [20,50,100]%, wall clock ~35 min.
- `results/run-001/live_benchmark_results.json` — per-checkpoint/per-arm rows with
  mean_recall, mean_precision, mean_judge_overall, cost_per_1000_queries, stage timings
  (retrieval_and_final_answer, llm_judge). Confirmed exact figures match README table
  (e.g. 20pct/hivemind: recall 0.0625, precision 0.1875, judge 3.594, cost/1k $0.0891).
- `results/run-001/chart.txt` — ASCII recall/precision chart, consistent with JSON/README.

## Conclusion
All four required write-up sections (architecture, novelty vs. prior art, benchmark
results, lessons learned) can be grounded entirely in existing docs and the real
run-001 artifacts. No source-code deep-dive was necessary beyond the two narrow LLD-cited
loadtest/OCI pointers already surfaced above. No invented numbers or hypothetical lessons
are needed.
