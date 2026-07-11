# Architecture discovery — subtask 4.5.19.1

Read order followed: `.cdr/index/*.jsonl` -> `.cdr/memory/pending.md` (n/a here, checked via
regression.jsonl) -> `docs/LLD/llm-provider.md` + `docs/LLD/eval.md` -> touched source files.

## Index findings

- `.cdr/index/feature.jsonl` line 54: subtask 5.3.3 (`agents/eval/cost_latency.py`,
  commit `50c2b06`) shipped `StageRecord`/`aggregate_by_stage`/`rollup_cost_per_1000_queries`,
  with an explicitly disclosed forward gap: no `agents/llm/` client emits `cost_usd`/token
  usage, and no pricing table exists.
- `.cdr/index/regression.jsonl` line 188 (run `175-verification`): confirms the same gap as a
  `forward_dependency_gap`, non-blocking for 5.3.3 itself but a real blocker for 5.3.4.
- `.cdr/index/task.jsonl` line 173 (`task-5.3.3`): same finding recorded as F1, "MUST be
  resolved when scoping 5.3.4".
- No existing `task-*` entry for subtask 4.5.19.1 / issue #59 (this is new work). Note: a
  **different**, unrelated `task-4.5.19.1` id already exists in `task.jsonl` line 150 for a Go
  storage-engine fix (`idalloc.go`, issue #58) — this is a pre-existing subtask-id collision in
  the index (same numeric id reused across an unrelated Go-engine issue and this Python
  LLM-interceptor issue). Confirmed via `gh issue view 59` that this run's subtask really is
  4.5.19.1 of issue #59 ("Python-side LLM interceptor"), not issue #58. Not this run's job to
  fix the index collision; flagged for the commit/handoff notes only.

## LLD findings

- `docs/LLD/llm-provider.md`: `agents/llm/` is provider-agnostic via the `LLMClient` ABC; only
  `agents/llm/*_client.py` may call a provider SDK/HTTP API directly. Explicitly names
  "per-call latency/cost interceptors here feed the benchmark's cost/latency metrics" as
  `agents/eval/`'s consumer of this seam. Also has a **binding security requirement**: any
  logging/tracing/interceptor layer built at this seam MUST NOT log/redact-fail the Gemini
  `?key=` query string (full query string must be redacted, not just headers) — this interceptor
  does not do HTTP-level logging at all (it wraps typed Python calls, not raw HTTP), so this risk
  does not apply directly, but is worth re-disclosing since the LLD explicitly calls out "the
  most likely place one would eventually be added".
- `docs/LLD/eval.md`: confirms `agents/llm/` is the "shared final-answer LLM and per-call
  cost/latency interceptor data source" for `agents/eval/`.

## Source findings

- `agents/llm/client.py`: `LLMClient` ABC, single abstract method
  `complete(prompt, *, model=None, temperature=0.0, max_tokens=None, timeout=None) -> str`.
  Deliberately minimal (str in, str out) — no usage/telemetry surface at all today.
- `agents/llm/ollama_client.py`: `OllamaClient.complete()` calls `/api/generate`, parses only
  `response["response"]`. Ollama's real API does return `eval_count`/`prompt_eval_count`, but
  since Ollama cost is always `$0.0` per the established 5.3.3 convention, no token-usage
  surfacing is needed here — left untouched.
- `agents/llm/openrouter_client.py`: `OpenRouterClient.complete()` calls
  `/chat/completions` (OpenAI-compatible), parses only `choices[0].message.content`.
  OpenRouter's real response includes a top-level `usage: {prompt_tokens, completion_tokens,
  total_tokens}` object that is currently parsed out of `data` but **entirely discarded** —
  confirmed by reading `complete()` end-to-end: `data = response.json()` is fully available:
  `usage` is a sibling of `choices` in the same dict, never touched again after `choices` is
  read. This is exactly the "if a client doesn't currently surface token counts, extend it
  minimally" case the issue describes.
- `agents/llm/gemini_client.py`: `GeminiClient.complete()` calls
  `models/{model}:generateContent`, parses only `candidates[0].content.parts[0].text`.
  Gemini's real response includes a top-level `usageMetadata: {promptTokenCount,
  candidatesTokenCount}` object, also a sibling of `candidates` in the same `data` dict, also
  fully discarded today. Same situation as OpenRouter.
- `agents/llm/factory.py`: `create_llm_client(provider, **kwargs)` returns a concrete
  `LLMClient`; does not attach a `.provider` attribute to instances, so the interceptor cannot
  introspect "which provider is this client" from the instance alone — the provider name must be
  passed explicitly by the caller (matches `StageRecord.provider` already being a free-form
  caller-supplied string, not something struct-derived).
- `agents/eval/cost_latency.py`: `StageRecord(arm, stage, duration_seconds, provider,
  cost_usd=None, query_id=None)` is a frozen dataclass with `__post_init__` validation
  (`duration_seconds >= 0`, `cost_usd >= 0` if given). `resolve_cost_usd()` trusts an explicit
  `cost_usd`, defaults exactly `{"ollama"}` (case-insensitive) to `0.0`, and raises `ValueError`
  for any other provider missing `cost_usd` — so this interceptor MUST always populate
  `cost_usd` itself for non-Ollama providers (never leave it `None` and rely on downstream
  defaulting, since downstream has none beyond Ollama).

## Existing test conventions confirmed

- `agents/llm/test_openrouter_client.py` / presumably `test_gemini_client.py`: inject
  `httpx.MockTransport(handler)` via the `transport=` constructor kwarg — zero real network
  calls. This run's new tests reuse the identical pattern.
- `agents/eval/test_cost_latency_aggregation.py` exists as the reference for how `StageRecord`
  fixtures/aggregation are exercised in tests; this run's integration test follows the same
  shape (construct `StageRecord`s, feed to `aggregate_by_stage`/`rollup_cost_per_1000_queries`).
