# Plan — subtask 4.5.19.1

## 1. `agents/llm/client.py` (additive)

- Add `@dataclass(frozen=True) TokenUsage(prompt_tokens: int, completion_tokens: int)`.
- Add `@dataclass(frozen=True) CompletionResult(text: str, model: str, usage: TokenUsage | None = None)`.
- Add a **concrete** (not `@abc.abstractmethod`) method on `LLMClient`:
  `complete_with_usage(self, prompt, *, model=None, temperature=0.0, max_tokens=None,
  timeout=None) -> CompletionResult` whose default body calls `self.complete(...)` and returns
  `CompletionResult(text=result, model=model or getattr(self, "_model", ""), usage=None)`.
  Any subclass that does not override it (Ollama) transparently gets "no usage data" behavior,
  which is fine since Ollama's cost is always `$0.0`.

## 2. `agents/llm/openrouter_client.py` (refactor, behavior-preserving)

- Extract the existing `complete()` body into `_do_complete(...) -> CompletionResult`.
- Add usage parsing: after the existing `choices[0].message.content` extraction succeeds, look
  at `data.get("usage")`; if it is a dict with integer `prompt_tokens`/`completion_tokens`,
  build a `TokenUsage`; otherwise `usage=None` (never raise for missing usage -- OpenRouter's
  contract for `content` is already strict, but `usage` is a bonus field this method should
  degrade gracefully without, since the acceptance criteria only requires computing cost *when*
  usage is present).
- `complete()` becomes `return self._do_complete(...).text` -- identical public signature,
  return type (`str`), and exception behavior (`_do_complete` raises the same
  `OpenRouterClientError`s at the same points).
- Override `complete_with_usage()` to just call and return `self._do_complete(...)`.

## 3. `agents/llm/gemini_client.py` (same refactor pattern)

- Extract into `_do_complete(...) -> CompletionResult`; parse `data.get("usageMetadata")` for
  `promptTokenCount`/`candidatesTokenCount` into a `TokenUsage` when present and well-typed,
  else `usage=None`.
- `complete()` / `complete_with_usage()` wired the same way as OpenRouter.

## 4. `agents/llm/interceptor.py` (new)

- `@dataclass(frozen=True) ModelRate(prompt_usd_per_1k: float, completion_usd_per_1k: float)`.
- `DEFAULT_RATE_TABLE: dict[str, dict[str, ModelRate]]` — illustrative, override via
  constructor — keyed by lowercase provider name, then exact model string, containing exactly
  the two models already named as this repo's defaults in `docs/LLD/llm-provider.md` /
  `openrouter_client.DEFAULT_MODEL` / `gemini_client.DEFAULT_MODEL`:
  `"openrouter": {"openai/gpt-4o-mini": ModelRate(...)}`,
  `"gemini": {"gemini-2.5-flash": ModelRate(...)}`. Explicitly documented in the module
  docstring as illustrative/example rates, not committed pricing, and that real model choice
  belongs to 5.3.2/5.3.4.
- `_FREE_PROVIDERS = frozenset({"ollama"})` (mirrors `cost_latency._FREE_LOCAL_PROVIDERS`).
- `class LLMInterceptorError(LLMError)`: raised when a non-free provider call cannot be priced
  (no usage data, or no rate-table entry for the resolved provider/model) -- fail loudly rather
  than emit a wrong/omitted cost.
- `@dataclass(frozen=True) InterceptedCompletion(text: str, record: StageRecord)`.
- `class LLMInterceptor`:
  - `__init__(self, *, rate_table: Mapping[str, Mapping[str, ModelRate]] | None = None)` —
    stores `rate_table or DEFAULT_RATE_TABLE` (injectable per constraint 4).
  - `def call(self, client: LLMClient, *, provider: str, arm: str, stage: str, prompt: str,
    model: str | None = None, query_id: str | None = None, **complete_kwargs) ->
    InterceptedCompletion`:
    1. `start = time.perf_counter()`
    2. `result = client.complete_with_usage(prompt, model=model, **complete_kwargs)`
    3. `duration = time.perf_counter() - start`
    4. `cost = self._resolve_cost(provider, result)`
    5. Build and return `InterceptedCompletion(text=result.text, record=StageRecord(arm=arm,
       stage=stage, duration_seconds=duration, provider=provider, cost_usd=cost,
       query_id=query_id))`.
  - `_resolve_cost(self, provider, result: CompletionResult) -> float`:
    - if `provider.lower()` in `_FREE_PROVIDERS`: return `0.0` unconditionally (ignore usage
      entirely, matches Ollama convention).
    - else: require `result.usage is not None`, else raise `LLMInterceptorError`; look up
      `self._rate_table.get(provider.lower(), {}).get(result.model)`, else raise
      `LLMInterceptorError` naming the missing provider/model; compute
      `usage.prompt_tokens/1000*rate.prompt_usd_per_1k +
      usage.completion_tokens/1000*rate.completion_usd_per_1k`.
- Import `StageRecord`/`LLMError` from `eval.cost_latency` / `llm.client` respectively (no
  changes to either).

## 5. `agents/llm/test_interceptor.py` (new)

- Unit: Ollama call via `LLMInterceptor.call(..., provider="ollama", ...)` against a mocked
  `OllamaClient` (mock transport) asserts `record.cost_usd == 0.0` and `record.duration_seconds
  >= 0` (measured, not asserted against a fixed value — real wall clock).
- Unit: OpenRouter mocked response including a realistic `usage` object asserts the exact
  computed `cost_usd` against a hand-computed expected value for the default-rate-table model.
- Unit: Gemini mocked response including a realistic `usageMetadata` object, same style.
- Unit: unknown/uncatalogued model for a paid provider raises `LLMInterceptorError`.
- Unit: duration measurement — inject an artificial delay in the mock transport handler (e.g.
  `time.sleep(0.01)`) and assert `duration_seconds` is at least that delay, proving direct
  measurement (not zero/estimated).
- Integration: build 3-4 `StageRecord`s from real `LLMInterceptor.call(...)` results (mixing
  ollama/openrouter/gemini, several arms/stages) and feed them straight into
  `eval.cost_latency.aggregate_by_stage` and `rollup_cost_per_1000_queries` unmodified, asserting
  sane, correctly-aggregated output -- proving interceptor output needs no adaptation layer.

## 6. Validation

- Run `pytest agents/llm/ agents/eval/ -v` (full existing suites + new file) and confirm zero
  regressions vs. baseline (pre-change) run.
- `ruff check` on new/changed files if ruff is configured/available.
