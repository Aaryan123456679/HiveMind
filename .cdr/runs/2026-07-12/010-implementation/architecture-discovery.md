# Architecture Discovery: task-5.3.5

## Existing, already-verified building blocks reused unchanged

- `agents/eval/run_benchmark.py`: `CorpusCheckpoint`, `checkpoint_corpus`/
  `build_checkpoints`, `ArmSpec`/`RetrieverFactory`/`HivemindRetrieverFn`,
  `default_arm_specs`, `JudgeConfig`, `BenchmarkReport`,
  `run_benchmark_with_traversal_precision`, `write_benchmark_results`.
  `main()` deliberately raises `RunBenchmarkError` and refuses live
  execution -- this subtask is the completion of that deliberately-deferred
  work, in a new file, not a modification of the existing one.
- `agents/eval/traversal_precision.py`: `compare_precision_across_checkpoints`
  (reused via `run_benchmark_with_traversal_precision`, unmodified).
- `agents/eval/chart.py`: `write_chart` (stdlib-only renderer, unmodified).
- `agents/eval/cost_latency.py`: `StageRecord`, `resolve_cost_usd`
  (unmodified).
- `agents/eval/ground_truth.py`: `DEFAULT_MANIFEST_PATH`, `QueryLabel`,
  `build_ground_truth_dataset`, `load_manifest` -- one deterministic query
  per topic derived from the manifest.
- `agents/llm/factory.create_llm_client` -- single factory entry point,
  provider resolved via explicit arg or `LLM_PROVIDER` env var.
- `agents/llm/interceptor.LLMInterceptor.call(...)` -- the single call path
  recording `StageRecord`s with cost/duration; subclassed (not modified) for
  cost-cap enforcement.
- `agents/query/pipeline.run_query_pipeline` / `PipelineError` -- unmodified.
- `agents/query/wiring.py`: `GrpcSearchCandidatesClient`,
  `GrpcGraphNeighborsClient`, `GrpcGetFileClient` -- real gRPC wrappers,
  unmodified.
- `agents/ingestion/wiring.GrpcPutSegmentClient.put_segment` -- unmodified.
- `agents/ingestion/normalize_pdf.normalize_pdf` -- unmodified.
- `engine/cmd/smokeserver` + the subprocess-launch/teardown pattern already
  established in `agents/ingestion/test_e2e_smoke.py` (`smokeserver_binary`
  / `running_engine` fixtures) -- pattern mirrored, not imported (this is a
  standalone script, not a pytest fixture consumer).

## Key design decision: stateful retriever across checkpoints

`default_arm_specs`'s internal `_build_hivemind_retriever_factory` wraps the
injected `hivemind_retriever: HivemindRetrieverFn` in a closure that simply
delegates every query call to the same callable object, once per
(checkpoint, arm) pair, without ever rebuilding it. Rather than fork or
modify `run_benchmark.py`'s orchestration loop to add an explicit
per-checkpoint engine-lifecycle hook, `LiveHivemindRetriever` is
implemented as a **stateful class**: its `__call__` compares the current
call's `corpus.keys()` against a cached signature; on change (i.e. moving to
the next checkpoint) it tears down the previous smokeserver process and
reprovisions a fresh one rooted at a new temp subdirectory, ingesting only
that checkpoint's docs. This satisfies "fresh engine per checkpoint"
without touching any already-verified sibling file.

## Key design decision: file_id -> doc_id mapping

`run_query_pipeline` returns engine-assigned integer `selected_file_ids`,
but ground truth / scoring expect string `doc_id`s. Solved by recording a
`file_id -> doc_id` dict at ingestion time from each `put_segment` result,
then translating back in `__call__`, silently skipping any unmapped id.

## Key design decision: cost cap enforcement

`run_benchmark_with_traversal_precision` has no cost-cap parameter. Solved
entirely inside `CostCappedInterceptor.call()` (subclass of
`LLMInterceptor`), the sole entry point for every paid judge call (routed
exclusively through `eval.llm_judge.score_answer` -> `interceptor.call`):
checks cumulative cost against the cap *before* delegating to
`super().call()`, raising `CostCapExceededError` (fail-closed, before the
real API call is attempted) once reached. No modification to
`run_benchmark.py` needed.

## Key design decision: judge client never wrapped (constraint d)

`_build_judge_config` never wraps `judge_llm_client` in
`ResilientLLMClient` for any `--judge-provider` value -- made statically/
unconditionally true rather than conditioned on provider, to keep the
invariant simple and directly testable.
