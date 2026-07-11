# Task 5.3.4 (issue #28): Corpus-growth-checkpoint degradation chart (20%/50%/100% ingested)

## Summary

Adds the benchmark orchestration harness and text-chart renderer that ties
together all prior issue #28 (milestone #7 benchmark suite) work into the
final deliverable: a corpus-growth-checkpoint degradation chart comparing
the 3 defined benchmark arms (`hivemind`, `vector_rag`, `graphrag_lite`) as
the ingested corpus grows through 20%/50%/100% checkpoints. Fourth and final
of four subtasks under issue #28 to land (5.3.1 metrics, 5.3.2 judge
scoring, 5.3.3 cost/latency rollup already verified and committed).

## Features

- `agents/eval/run_benchmark.py`: `checkpoint_corpus()` builds deterministic,
  ordered-prefix-superset corpus checkpoints (20% -> 2 docs, 50% -> 4 docs,
  100% -> 7 docs on the reference synthetic corpus, each checkpoint a strict
  growing subset of the next); benchmark orchestration wires the 3 resolved
  arms x 3 checkpoints through the existing `pipeline`/`metrics`/`llm_judge`/
  `cost_latency` machinery from 5.3.1-5.3.3 with no reimplementation.
  `main()` is a disclosed CLI entry point that refuses real/live execution
  (`RunBenchmarkError`) rather than silently running paid API calls.
- `agents/eval/chart.py`: stdlib-only text-table degradation-chart renderer
  over `run_benchmark`'s per-checkpoint, per-arm results -- zero new
  dependency, consistent with the rest of `agents/eval`'s stdlib-first
  convention.
- `agents/eval/test_run_benchmark.py` / `agents/eval/test_chart.py`: test
  spec coverage for checkpoint construction, arm resolution, `main()`'s
  refusal-to-execute-live path, and chart rendering.

## Impact

- Closes issue #28: all four subtasks (5.3.1 metrics, 5.3.2 judge scoring,
  5.3.3 cost/latency rollup, 5.3.4 this harness/chart) are now implemented
  and independently verified. This is the harness the orchestrator will next
  use to actually run the real, paid-API corpus-growth-checkpoint benchmark
  and produce the project's key novelty-result chart -- 5.3.4 itself makes
  zero live API calls (fixture/mock-tested only, matching the existing
  Phase 5 convention of code-then-gated-live-run).
- Zero regression surface: diff adds only the 4 new files above; no
  modification to `agents/eval/pipeline.py`, `agents/eval/metrics.py`,
  `agents/eval/llm_judge.py`, `agents/eval/cost_latency.py`, or
  `agents/llm/interceptor.py`.
- Resolves the forward-flag left by 5.4.1's verification (`.cdr/runs/2026-07-12/005-verification`):
  the verifier for this subtask independently constructed a genuine
  single-call, 3-checkpoint invocation and confirmed
  `compare_precision_across_checkpoints`/`checkpoints_with_precision_decrease`
  behave correctly at the real checkpoint cardinality 5.3.4 uses.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run:** `.cdr/runs/2026-07-12/008-verification` (`verification.json`)
- Independently reproduced against the verifier's own from-scratch
  instrumented fixture (a distinct 7-doc corpus from the implementer's):
  - "3 arms" correctly resolves to exactly `{hivemind, vector_rag,
    graphrag_lite}` -- `pipeline.py` genuinely defines only these 3 wrapper
    functions, no rerank variant included.
  - `checkpoint_corpus` produces correctly nested/growing/deterministic
    checkpoints (20% -> 2 docs, 50% -> 4 docs, 100% -> 7 docs, each an
    ordered-prefix superset of the previous).
  - **CRITICAL, independently confirmed exact:** the per-call-count
    disclosure formula is exactly accurate against the verifier's own
    call-counting stub clients (graphrag_lite: 22 calls matching formula;
    vector_rag: 12 embed calls matching formula; judge scoring: 9/9 calls
    matching formula exactly) -- no undercount risk for the real-money cost
    estimate the orchestrator builds next.
  - Judge scoring genuinely routes through `LLMInterceptor.call()` with zero
    bypass (`interceptor.call_count == judge_client.call_count == 9`
    exactly).
  - `main()` safely refuses real execution (raises `RunBenchmarkError`
    immediately), with one disclosed non-blocking caveat: `main()` does call
    the real but purely-local, zero-cost `build_ground_truth_dataset()`
    (reading the pre-existing `data/synthetic_corpus/generated/manifest.json`)
    before raising, with the result unused (confirmed via ruff F841) --
    touches no real corpus text or paid API, so doesn't undermine the
    zero-live-calls claim, just makes "main() does nothing but raise"
    slightly overstated.
  - Confirmed safe to build the real-money cost estimate on top of.
  - Full `agents/eval/` suite independently re-run: 163 passed.
- Non-blocking cosmetic findings (tracked below and in `.cdr/memory/pending.md`):
  7 ruff lint issues (5 unused imports in `test_run_benchmark.py`, 2 unused
  locals -- `percentages`, `dataset` -- in `main()`), and `chart.py`'s
  stdlib-only text-table renderer is a legitimate but materially weaker
  substitute for a real chart given this is billed as the project's key
  novelty result -- flagged for follow-up before any external presentation
  of results.

## Release Notes

Added the corpus-growth-checkpoint benchmark orchestration harness
(`agents/eval/run_benchmark.py`) and a stdlib-only degradation-chart
renderer (`agents/eval/chart.py`) for the milestone #7 benchmark suite
(issue #28, subtask 5.3.4 -- final subtask of #28). No live provider calls
are made by this subtask; the harness is fully fixture/mock-tested. The
actual paid-API corpus-growth-checkpoint benchmark run across the 3 arms
and 20%/50%/100% checkpoints, with judge scoring against both OpenRouter and
Gemini, is deferred to the orchestrator's next step under the user's
existing $7 OpenRouter / $9 Gemini spend caps.
