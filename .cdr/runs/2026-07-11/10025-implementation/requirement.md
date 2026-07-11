# Requirement — subtask 4.5.19.1 (issue #59, milestone #7 / Phase 5)

Title: Implement `agents/llm/interceptor.py`: wrap `LLMClient.complete()` calls to record
per-call duration + cost, emitting `cost_latency.StageRecord`-compatible records.

## Acceptance criteria (verbatim from `gh issue view 59`)

A thin, provider-agnostic interceptor wraps any `LLMClient.complete()` call (Ollama,
OpenRouter, Gemini) and records `duration_seconds` (measured directly, not estimated) plus a
`cost_usd` figure for paid providers.

- Ollama: `cost_usd` is always `0.0` (established convention from 5.3.3).
- OpenRouter/Gemini: cost is computed from the provider's actual reported token usage
  (prompt/completion tokens) multiplied by a small, explicitly-documented static per-model rate
  table (only for models this repo actually uses/plans to use for judging/benchmarking — do not
  invent pricing for unused models).
- If a client doesn't currently surface token counts, extend it minimally to do so (check
  response parsing first — these APIs typically already return usage in their JSON response).
- Records must be directly consumable by `agents/eval/cost_latency.aggregate_by_stage` /
  `rollup_cost_per_1000_queries` without modification to those functions.

## Test spec (verbatim)

`pytest agents/llm/test_interceptor.py`: mocked provider responses (including realistic
token-usage fields) assert correct duration measurement, correct `cost_usd` computation per
provider/model, and correct `$0.0` for Ollama; an integration-style test constructs a few
`StageRecord`s from interceptor output and feeds them through `agents/eval/cost_latency.py`'s
existing functions to confirm compatibility.

## Impacted modules (per issue)

`agents/llm/interceptor.py`, `agents/llm/test_interceptor.py`, `agents/llm/openrouter_client.py`,
`agents/llm/gemini_client.py` (only if token-usage parsing needs extending).

## Standing constraints (from launching agent, must follow exactly)

1. No live API calls anywhere — everything mocked (`httpx.MockTransport`), matching existing
   `agents/llm/` test convention.
2. No new dependency — `agents/pyproject.toml` untouched; stdlib `time.perf_counter()` +
   dict-based rate lookups only.
3. Zero regressions in existing `agents/llm/` and `agents/eval/` suites; any extension to
   `openrouter_client.py`/`gemini_client.py` must be backward-compatible (existing `complete()`
   call signature/behavior unchanged).
4. Standard 9-step CDR workflow; ONE local commit, no push.
5. This agent does not verify its own work (invariant I4) — deferred to `/cdr:verify`.
