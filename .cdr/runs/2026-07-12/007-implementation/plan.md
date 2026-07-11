# Plan -- subtask 5.3.4

1. `agents/eval/run_benchmark.py`:
   - `CorpusCheckpoint` dataclass (`label`, `pct`, `docs: list[tuple[str,str]]`).
   - `checkpoint_corpus(all_docs, pct)` -- minimal percentage-prefix corpus slicer (new, since
     no existing utility found in `agents/eval/`/`agents/ingestion/`).
   - `build_checkpoints(all_docs, pcts=(20,50,100))` -- convenience wrapper.
   - `ArmSpec` dataclass bundling an arm name + a `build_retriever(corpus_map, checkpoint) ->
     Callable[[QueryLabel], list[str]]` factory (index/graph built once per checkpoint, reused
     across all queries at that checkpoint).
   - `default_arm_specs(...)` -- constructs the 3 canonical `ArmSpec`s (hivemind injected
     retriever, vector_rag via `VectorRagIndex`/`chunk_document`, graphrag_lite via
     `EntityGraph.build`), reusing `eval.baselines.vector_rag`/`graphrag_lite`'s existing
     `retrieve_documents` -- no reimplementation.
   - `run_arm_at_checkpoint(...)` -- runs one arm's retriever over every query, times each call,
     scores via `eval.metrics.score_arm`, builds `StageRecord`s (`provider="ollama"`), rolls up
     cost via `eval.cost_latency.rollup_cost_per_1000_queries`. Optional `judge_config` param
     wires `eval.pipeline.generate_final_answer` (shared final-answer path) +
     `eval.llm_judge.score_arm_answers` (which itself calls `LLMInterceptor.call()`) -- disabled
     by default (`None`).
   - `run_benchmark(checkpoints, queries, arm_specs, *, k, ...)` -- top-level orchestration,
     returns a `BenchmarkReport` (list of per-checkpoint-per-arm rows + all stage records).
   - `write_benchmark_results` / `load_benchmark_results` -- JSON I/O.
   - `main()` CLI: `--checkpoints 20,50,100`, `--manifest`, `--ground-truth`, `--out`,
     `--enable-judge` (off by default). Not exercised end-to-end in this pass's own tests (would
     require a real corpus + real Ollama server) -- built and reviewable, but undemonstrated
     live.
   - Reuse (not duplicate) `eval.traversal_precision.CorpusGrowthCheckpoint` /
     `compare_precision_across_checkpoints` for the optional graph-expansion-precision check
     per checkpoint (5.4.1), wiring `CorpusCheckpoint.docs` straight into
     `CorpusGrowthCheckpoint(label=..., docs=...)`.

2. `agents/eval/chart.py`:
   - `render_degradation_table(rows)` -- stdlib-only text table, one row per checkpoint, one
     column pair (recall, precision) per arm, sorted by `checkpoint_pct` ascending.
   - `write_chart(rows, path)` -- writes the rendered table to a `.txt` file.
   - No new dependency; matplotlib not introduced (not in `agents/pyproject.toml`, would be
     scope creep per this subtask's own instruction).

3. `agents/eval/test_run_benchmark.py`:
   - Tiny synthetic 5-doc corpus + `ground_truth`-shaped `QueryLabel`s (fixture, no
     `data/synthetic_corpus/` dependency).
   - Stub `LLMClient` (canned entity-extraction + final-answer JSON, mirroring
     `test_graphrag_baseline.py`/`test_shared_final_llm.py`'s established convention) and a
     `transport=httpx.MockTransport`-backed embedding stand-in (or a lightweight fake
     `OllamaEmbeddingClient`-compatible object) -- zero real network calls.
   - Build 3 synthetic checkpoints (20/50/100pct) over the fixture corpus via
     `build_checkpoints`.
   - Run `run_benchmark(...)` with `default_arm_specs(...)` (injected fake hivemind retriever).
   - Assert: output has exactly 3 checkpoints x 3 arms = 9 rows; every row has well-formed
     `mean_recall`/`mean_precision` in `[0,1]` and a cost/latency summary.
   - Round-trip `write_benchmark_results`/`load_benchmark_results`.
   - Separate test: call `compare_precision_across_checkpoints()` directly with a real 3-item
     `CorpusGrowthCheckpoint` list (closing the 5.4.1 multi-checkpoint test gap per the
     launching agent's instruction), asserting per-checkpoint independence.
   - Separate test (opt-in judge path): exercise `judge_config` wiring with a stub judge
     `LLMClient` + a real `LLMInterceptor` instance (still zero network -- stub client, no
     `httpx`/Ollama call), asserting judge `StageRecord`s show up in the aggregated cost.

4. `agents/eval/test_chart.py`:
   - Feed `render_degradation_table` a small fixed 3-checkpoint x 3-arm row list; assert the
     rendered text contains all three checkpoint labels, all three arm names, and correctly
     formatted recall/precision figures.

5. Self-consistency: run the full `agents/eval/` pytest suite (existing + 2 new files) offline;
   confirm zero new dependency; confirm no `.env` read / no live network call anywhere in the
   new test files (grep for `httpx.Client(` without `transport=`, `OpenRouterClient`,
   `GeminiClient`, `os.environ`).

6. One local commit (Problem/Solution/Impact style), no push.

7. Handoff with explicit disclosures (never-run-live, cost-estimate inputs, 3-vs-4-arm
   resolution, 5.4.1 multi-checkpoint test).
