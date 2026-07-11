# Requirement — Subtask 5.3.2 (issue #28)

Source: `gh issue view 28` (Phase 5: Benchmark suite epic), subtask 5.3.2, pulled verbatim.

## Subtask text

**5.3.2 — LLM-judge answer-quality scoring + manual spot-check calibration harness**

- Acceptance criteria: An LLM-judge scoring pass rates answer quality on a defined rubric, with
  a manual spot-check harness allowing a human to compare judge scores against a sample of
  manually-rated answers for calibration.
- Test spec: `pytest agents/eval/test_llm_judge.py` (LLM-judge call mocked): assert scoring
  pipeline produces expected score structure; a manual calibration script is runnable and
  produces a comparison report.
- Impacted modules: `agents/eval/llm_judge.py`, `agents/eval/test_llm_judge.py`,
  `agents/eval/calibrate_judge.py`

"Each subtask above is sized to exactly one commit."

## Prior-state confirmation (per launching agent)

This subtask was previously started and explicitly stopped mid-flight by the user before any
commit was made. `git log` (HEAD `690a571`) has no trace of it. Confirmed clean on disk: no
`agents/eval/llm_judge.py`, `agents/eval/calibrate_judge.py`, or `agents/eval/test_llm_judge.py`
exist. This run starts fresh, not resuming partial work.

## Scoping constraint (imposed by launching agent, binding for this run)

CODE-ONLY. No live calls to OpenRouter or Gemini during this implementation/test pass (real,
budget-capped keys now live in `.env`; must not spend that budget). All tests must use mocked
LLM clients (`httpx.MockTransport`, matching `agents/llm/test_openrouter_client.py` /
`agents/llm/test_gemini_client.py` / `agents/llm/test_interceptor.py` convention). The manual
calibration harness must be implemented and tested with mocks only; if the issue's test spec
implies a live calibration run against real judge-model outputs, that is explicitly deferred to
a separate, deliberately-scoped run with its own cost estimate, disclosed in the handoff.

## Reuse/integration guidance (from launching agent, to honor established conventions)

- Judge LLM calls should be cost/latency-tracked via `agents/llm/interceptor.py`'s
  `LLMInterceptor.call()` (issue #59, landed at `84415ad`/`690a571`), not called via
  `LLMClient.complete()`/`complete_with_usage()` directly — so judge-call cost rolls into the
  existing `agents/eval/cost_latency.py` aggregation pipeline (5.3.3 precedent).
- Follow `agents/eval/pipeline.py`'s `generate_final_answer` pattern (one shared function, no
  per-caller divergence) and `agents/eval/metrics.py`'s dataclass-based scoring-structure
  pattern (5.3.1) as architectural precedent for how the new judge-scoring module should be
  shaped and tested.
