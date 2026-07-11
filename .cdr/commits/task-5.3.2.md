# Task 5.3.2 (issue #28): LLM-judge answer-quality scoring + manual calibration harness

## Summary

Adds an LLM-judge scoring pass for benchmark answer quality (issue #28,
milestone #7's benchmark suite), plus a manual spot-check calibration harness
so a human can compare judge scores against manually-rated answers. Second of
four subtasks under issue #28 to land (5.3.1 metrics, 5.3.3 cost/latency
rollup already verified and committed).

## Features

- `agents/eval/llm_judge.py`: three-criterion rubric (correctness,
  completeness, citation_accuracy; 1-5 scale each), a single shared
  `score_answer()` call path mirroring the existing
  `pipeline.generate_final_answer` precedent, routed exclusively through
  `LLMInterceptor.call()` (issue #59) so judge cost/latency rolls straight
  into the existing `cost_latency.aggregate_by_stage` /
  `rollup_cost_per_1000_queries` machinery with zero adaptation.
  `parse_judge_response()` raises `JudgeError` on any malformed, missing, or
  out-of-range judge output rather than inventing or clamping a score,
  matching `cost_latency.py`'s established "refuse to invent data"
  convention. `score_arm_answers()` scores a whole arm's answers per query.
- `agents/eval/calibrate_judge.py`: manual-spot-check calibration harness --
  `ManualRating`/`CalibrationSample`/`CalibrationReport` dataclasses, a pure
  `run_calibration()` core (delta, mean/max absolute delta,
  agreement-within-1-point statistics), `load_manual_ratings`/`write_report`
  I/O helpers, and a CLI entry point. All live-provider client construction
  is confined to `main()`'s disclosed CLI boundary -- no import-time or
  core-function API dependency.
- `agents/eval/test_llm_judge.py`: test spec coverage (LLM-judge call
  mocked) plus calibration-harness runnability/report-shape tests.

## Impact

- Closes the scoring gap between issue #28's already-shipped topic-level
  metrics (5.3.1) and cost/latency rollup (5.3.3): benchmark answer quality
  can now be scored on a defined rubric with the same disclosed,
  non-inventing error-handling posture used elsewhere in `agents/eval`.
- Zero regression surface: diff touches only the 3 new files above; no
  modification to `agents/llm/interceptor.py`, `agents/eval/cost_latency.py`,
  `agents/eval/metrics.py`, or `agents/llm/client.py`.
- Zero live API calls in this subtask -- judge scoring and calibration are
  fully exercised against mocks/stubs per the issue's test spec. The actual
  paid-API judge/calibration run against real OpenRouter/Gemini output
  remains deferred to subtask 5.3.4, gated by the user's explicit spend caps
  ($7 OpenRouter / $9 Gemini, minimal-usage preference).
- One forward-looking non-blocking flag for 5.3.4: `parse_judge_response`
  requires strict JSON with zero tolerance for markdown-fence- or
  prose-wrapped output. Real judge-model output (GPT-4o-mini, Gemini)
  sometimes wraps JSON in prose despite strict-JSON prompting, which could
  drive a higher-than-expected `JudgeError` rate once real API budget is
  spent. Tracked in `.cdr/memory/pending.md` for 5.3.4 to budget for.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run:** `.cdr/runs/2026-07-12/002-verification` (`verification.json`)
- Independently reproduced: `JudgeError` fires correctly on 8
  independently-authored malformed-input constructions (missing key,
  out-of-range score 0/6, non-numeric, float, null, empty string, JSON
  wrapped in prose) -- never silently defaults or clamps.
- Interceptor integration confirmed real via a tripwire `LLMClient` stub:
  `complete()` never called, only `complete_with_usage()` via
  `LLMInterceptor.call()`; resulting `StageRecord` fed through
  `aggregate_by_stage`/`rollup_cost_per_1000_queries` with zero adaptation.
- Calibration statistics hand-verified exactly correct on the verifier's own
  5-sample fixture.
- Zero live API calls confirmed (no `OpenRouterClient`/`GeminiClient`/env-key
  references outside `main()`'s disclosed CLI boundary).
- Full suite re-run: 230 passed, ruff clean, `agents/pyproject.toml` diff
  empty (stdlib-only). Zero regression risk (diff touches only the 3 new
  files).

## Release Notes

Added an LLM-judge answer-quality scoring pass and a manual calibration
harness for the milestone #7 benchmark suite (issue #28, subtask 5.3.2). No
live provider calls are made by this subtask; scoring and calibration are
fully mock/stub-tested. Real paid-API judge calibration is deferred to
subtask 5.3.4 under the user's existing spend caps.
