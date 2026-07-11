# Architecture Discovery — Subtask 5.3.2

## Index findings (read before source, per protocol)

- `.cdr/index/task.jsonl`: `task-5.3.1` (issue 28, commit 39bd2dd, PASS_WITH_COMMENTS) —
  canonicalized `recall_at_k`/`precision_at_k` into `agents/eval/metrics.py`. `task-5.3.3`
  (issue 28, commit 50c2b06, PASS_WITH_COMMENTS) — `agents/eval/cost_latency.py`
  `StageRecord`/`aggregate_by_stage`/`rollup_cost_per_1000_queries`. No `task-5.3.2` entry
  exists yet — confirms clean/fresh start.
- `.cdr/index/feature.jsonl`: `eval/` = "Benchmark harness against vector-RAG and GraphRAG
  baselines" ([LLD/eval.md](../../../docs/LLD/eval.md)).
- No prior decision/file index entries reference `llm_judge`, `calibrate_judge`, or
  `judge_scoring` — nothing to reconcile against.

## docs/HLD.md

- `agents/eval/` = benchmark harness against vector-RAG/GraphRAG baselines
  ([LLD/eval.md](../../../docs/LLD/eval.md)).
- Repo layout: `agents/` = Python (`ingestion/`, `query/`, `llm/`, `eval/`).
- No judge-rubric detail lives in HLD; it defers to LLD.

## docs/LLD/eval.md

- "Metrics" section names, verbatim: "LLM-judge answer quality + manual spot-check
  calibration" as one of the benchmark's headline metrics, alongside recall/precision@k,
  per-stage latency, and $/1000-query cost.
- "Interactions with other modules": `agents/llm/` = "shared final-answer LLM and per-call
  cost/latency interceptor data source" — i.e. the LLD itself already names the interceptor as
  the intended integration point for any paid LLM call made from `agents/eval/`, which is
  exactly what the launching agent's wiring instruction restates.
- Cross-reference: `llm-provider.md` — "shared final-answer LLM + cost/latency interceptors".
- No rubric text is defined in the LLD (it says only "LLM-judge answer quality"); rubric design
  is left to this subtask, same pattern as 5.3.1's LLD-silent "include_cross_reference"
  simplification and 5.3.3's LLD-silent "no pricing table" disclosure — both resolved by
  disclosed implementation choices in-module, not doc edits.

## Touched-file precedent (5.3.1, 5.3.3, pipeline.py, interceptor.py)

- `agents/eval/metrics.py` (5.3.1): frozen dataclasses (`QueryScore`, `ArmScore`) for score
  structure; pure functions (`score_query`, `score_arm`) that take/return dataclasses; verbose
  disclosure-style module + function docstrings citing the issue/subtask number, scope
  boundaries, and reuse decisions explicitly.
- `agents/eval/cost_latency.py` (5.3.3): frozen dataclasses (`StageRecord`, `StageAggregate`,
  `ArmCostSummary`); "refuse to invent data" philosophy (`resolve_cost_usd` raises rather than
  guessing pricing) — a convention this module's judge-scoring should mirror for anything it
  cannot honestly compute (e.g. don't invent a judge score if the mocked/real call fails).
- `agents/eval/pipeline.py` (5.2.4): single shared function (`generate_final_answer`) that every
  caller funnels through — architectural precedent explicitly named by the launching agent for
  how the new judge-scoring module should structure its "one call path" for scoring an
  answer.
- `agents/llm/interceptor.py` (4.5.19.1, issue #59): `LLMInterceptor.call(client, *, provider,
  arm, stage, prompt, model=None, query_id=None, temperature=0.0, max_tokens=None,
  timeout=None) -> InterceptedCompletion(text, record: StageRecord)`. This is the call surface
  judge-scoring must route through instead of `client.complete()`/`complete_with_usage()`
  directly, per the launching agent's wiring instruction and the LLD's own "interceptor is the
  cost/latency data source" framing.
- `agents/llm/client.py`: `LLMClient` ABC, `complete()` abstract, `complete_with_usage()`
  concrete-with-default. `CompletionResult`, `TokenUsage`.
- `agents/eval/ground_truth.py` (5.1.3): `RelevantDoc`, `QueryLabel` shapes — not directly
  needed by judge-scoring (judge scores an *answer* against a *query*, not a doc-retrieval
  list), but establishes the project's "frozen dataclass + `to_json()`" serialization
  convention used across `agents/eval/`.

## Test convention precedent

- `agents/llm/test_openrouter_client.py`, `test_gemini_client.py`, `test_interceptor.py`: all
  HTTP interception via `httpx.MockTransport` injected as `transport=` kwarg on the client
  constructor — never real network. This is the exact mocking mechanism the launching agent's
  scoping constraint requires be reused for `test_llm_judge.py`.
- `agents/eval/test_shared_final_llm.py`: uses a hand-written `_SpyLLMClient(LLMClient)` stub
  (not `httpx.MockTransport`) when the module under test is a pure orchestration layer above
  `LLMClient`, not a provider client itself — i.e. `agents/eval/` tests commonly stub at the
  `LLMClient` interface level rather than the HTTP layer, since `agents/eval/` never
  constructs a provider client itself (Ollama-only precedent per `pipeline.py`'s own
  docstring). `llm_judge.py` will follow this same "stub LLMClient, don't reach into HTTP"
  convention for its own tests, while `test_interceptor.py`'s `httpx.MockTransport` precedent
  is what a lower-level `agents/llm/` test would use — both satisfy the launching agent's "mocked
  LLM clients" requirement, just at different call-stack layers appropriate to the module under
  test.

## Conclusion — where this subtask's code lives

- `agents/eval/llm_judge.py`: rubric-based judge-scoring module. Defines the rubric/prompt
  template, calls the judge LLM via `LLMInterceptor.call()` (not raw `complete()`), parses the
  judge's structured response into a frozen-dataclass score structure (mirroring
  `metrics.py`'s `QueryScore`/`ArmScore` pattern), and exposes one shared scoring function
  analogous to `pipeline.py`'s `generate_final_answer` (single call path, no per-caller
  divergence).
- `agents/eval/calibrate_judge.py`: manual spot-check calibration script/harness — reads a
  sample of manually-rated (human-scored) answers plus their judge scores, and produces a
  comparison report (e.g. agreement/delta stats). Runnable as `python -m eval.calibrate_judge`
  (mirroring `ground_truth.py`'s `argparse` + `main()` CLI convention).
- `agents/eval/test_llm_judge.py`: mocked-`LLMClient`-stub tests (mirroring
  `test_shared_final_llm.py`'s `_SpyLLMClient` convention, plus a use of `LLMInterceptor` with
  an `httpx.MockTransport`-backed client to prove the cost/latency wiring produces a valid
  `StageRecord`) asserting score structure + a runnable calibration script producing a
  comparison report, entirely offline.
