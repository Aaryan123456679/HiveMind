# Plan — Subtask 5.3.2

## 1. `agents/eval/llm_judge.py`

- Module docstring: issue #28 subtask 5.3.2, disclosed design (rubric choice, interceptor
  wiring, no-live-call constraint for this pass), cross-references to `metrics.py`/`pipeline.py`
  precedent per launching agent's guidance.
- `JUDGE_RUBRIC_CRITERIA: tuple[str, ...]` = `("correctness", "completeness",
  "citation_accuracy")` — a small, defined rubric (acceptance criteria: "rates answer quality on
  a defined rubric"). Each criterion scored 1-5 (documented scale).
- `JudgeError(Exception)` — raised on malformed/missing judge output; never silently invents a
  score (mirrors `cost_latency.py`'s "refuse to invent data" convention).
- `build_judge_prompt(query, answer, *, reference_context="") -> str` — renders the rubric +
  query + answer (+ optional reference context) into a prompt instructing the judge to respond
  with strict JSON `{"scores": {criterion: int}, "rationale": str}`.
- `JudgeScore` (frozen dataclass): `query: str`, `answer: str`, `scores: Mapping[str,int]`,
  `overall: float` (mean of `scores.values()`), `rationale: str`. `to_json()` method (matches
  `ground_truth.py`/`metrics.py` serialization convention).
- `parse_judge_response(query, answer, raw_text) -> JudgeScore` — parses `raw_text` as JSON,
  validates every `JUDGE_RUBRIC_CRITERIA` key present with an int in `[1, 5]`, computes
  `overall`, raises `JudgeError` on any structural problem (missing key, out-of-range score,
  invalid JSON).
- `score_answer(query, answer, llm_client, interceptor, *, arm, stage="llm_judge",
  provider="ollama", model=None, query_id=None, reference_context="") -> JudgeScoringResult`
  — the single shared scoring call path (mirrors `pipeline.py`'s `generate_final_answer`
  pattern: one function, every caller funnels through it). Builds the prompt, calls
  `interceptor.call(llm_client, provider=provider, arm=arm, stage=stage, prompt=...,
  model=model, query_id=query_id)`, parses the result text into a `JudgeScore` via
  `parse_judge_response`. Returns `JudgeScoringResult(score: JudgeScore, record: StageRecord)`
  (re-exposing the `StageRecord` so callers can feed it straight into
  `cost_latency.aggregate_by_stage`/`rollup_cost_per_1000_queries`, no adaptation layer, per
  `InterceptedCompletion`'s own precedent).
- `score_arm_answers(arm_name, answers: Mapping[str,str] (query->answer), llm_client,
  interceptor, *, provider="ollama", model=None) -> list[JudgeScoringResult]` — scores a whole
  arm's answers, mirroring `metrics.score_arm`'s "one call per query, same shape" pattern (not
  a literal reuse of `ArmScore`, since judge scores are a different structure, but the same
  "per-arm loop over one shared per-item function" architecture).

## 2. `agents/eval/calibrate_judge.py`

- Module docstring: manual spot-check calibration harness per subtask 5.3.2's acceptance
  criteria ("manual spot-check harness allowing a human to compare judge scores against a
  sample of manually-rated answers for calibration").
- `ManualRating` (frozen dataclass): `query: str`, `answer: str`, `human_score: float` (1-5
  scale, matching the judge's own per-criterion scale for direct comparability against
  `JudgeScore.overall`), `human_rationale: str = ""`.
- `CalibrationSample` (frozen dataclass): `manual: ManualRating`, `judge: JudgeScore`,
  `delta: float` (`judge.overall - manual.human_score`).
- `CalibrationReport` (frozen dataclass): `samples: list[CalibrationSample]`, `n: int`,
  `mean_absolute_delta: float`, `max_absolute_delta: float`,
  `agreement_within_1_point: float` (fraction of samples with `abs(delta) <= 1.0`).
  `to_json()` method.
- `run_calibration(manual_ratings: list[ManualRating], llm_client, interceptor, *,
  arm="calibration", model=None) -> CalibrationReport` — the pure, testable core: for each
  `ManualRating`, calls `llm_judge.score_answer(...)`, builds a `CalibrationSample`, then
  aggregates into a `CalibrationReport`. No file I/O, no argparse -- fully unit-testable with a
  stub client.
- `load_manual_ratings(path) -> list[ManualRating]` / `write_report(report, path) -> None` —
  JSON load/write helpers (mirrors `ground_truth.py`'s `load_ground_truth`/
  `write_ground_truth` convention).
- `main(argv=None) -> None` — argparse CLI (`--ratings`, `--out`, `--provider`, `--model`)
  mirroring `ground_truth.py`'s `main()` shape: loads manual ratings, constructs a real
  `LLMClient` via `llm.factory.create_llm_client(provider)` (only reached when the script is
  actually invoked -- never called by the test suite with a real provider), wraps it with a
  fresh `LLMInterceptor()`, runs calibration, writes + prints a summary. This makes the script
  itself "runnable" per the test spec while keeping all real-network code behind a function
  boundary the tests don't exercise except via a monkeypatched client factory.

## 3. `agents/eval/test_llm_judge.py`

Covers both `llm_judge.py` and `calibrate_judge.py` (single test file per the issue's own
impacted-modules list, which names only `test_llm_judge.py`).

- `_StubLLMClient(LLMClient)` — same convention as `test_shared_final_llm.py`'s `_SpyLLMClient`:
  records calls, returns a canned JSON judge-response string.
- Prompt-building: asserts rubric criteria names appear in `build_judge_prompt`'s output.
- `parse_judge_response`: valid JSON -> correct `JudgeScore` + `overall` mean; missing
  criterion -> `JudgeError`; out-of-range score -> `JudgeError`; invalid JSON -> `JudgeError`.
- `score_answer`: with `_StubLLMClient` + real `LLMInterceptor(rate_table=...)` and
  `provider="ollama"` (free, so no rate-table entry needed) -> asserts returned
  `JudgeScoringResult.score` matches expected structure and `.record` is a valid `StageRecord`
  usable directly by `cost_latency.aggregate_by_stage`.
- Interceptor-integration proof (mirrors `test_interceptor.py`'s own httpx-level test): builds
  an `OllamaClient(transport=httpx.MockTransport(handler))` returning a canned judge JSON
  response, calls `score_answer` through it, asserts a real `StageRecord` with measured
  `duration_seconds` and `cost_usd == 0.0` -- proves the "route through LLMInterceptor, not
  complete() directly" wiring end-to-end, still fully offline (`httpx.MockTransport`, no real
  socket).
- `score_arm_answers`: multiple queries -> one `JudgeScoringResult` per query, same order.
- `run_calibration`: fixture `ManualRating` list + `_StubLLMClient` -> asserts `CalibrationReport`
  structure (`n`, deltas, `mean_absolute_delta`, `agreement_within_1_point`) computed correctly
  by hand for a small fixture (matching `test_cost_latency_aggregation.py`'s "hand-verified,
  literal fixture, `==` assertion" convention).
- `load_manual_ratings`/`write_report` round-trip via `tmp_path`.
- CLI smoke test: `main()` invoked with `monkeypatch` replacing
  `calibrate_judge.create_llm_client` with a function returning `_StubLLMClient()`, `--ratings`
  pointing at a `tmp_path` fixture file, `--out` a `tmp_path` output path -- asserts the script
  runs end-to-end and writes a well-formed comparison-report JSON file. No real provider, no
  network, no `.env` read.

## Explicitly deferred (disclosed in handoff, not silently dropped)

- A live calibration run against real judge-model (OpenRouter/Gemini) outputs and real
  manually-rated answers is **not** performed in this pass -- deferred to a separate,
  deliberately-scoped run the orchestrator will execute later with explicit cost estimation
  first, per the launching agent's binding scoping constraint.
- Wiring `score_arm_answers` into an actual benchmark run (`agents/eval/run_benchmark.py`,
  subtask 5.3.4, not yet built) is out of scope here, mirroring 5.3.1/5.3.3's own disclosed
  "fixture-only, not yet wired to a real corpus run" scope boundary.
