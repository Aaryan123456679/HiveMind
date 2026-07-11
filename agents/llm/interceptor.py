"""`LLMInterceptor`: a thin, provider-agnostic wrapper around `LLMClient.complete()` calls that
records per-call `duration_seconds` and `cost_usd`, emitting `agents.eval.cost_latency.StageRecord`
-compatible records.

Per issue #59 subtask 4.5.19.1. This closes the forward gap subtask 5.3.3 (`agents/eval/
cost_latency.py`) explicitly disclosed and deferred: `cost_latency.StageRecord`/
`aggregate_by_stage`/`rollup_cost_per_1000_queries` define the aggregation/rollup shape a real
interceptor would need to emit, but nothing in the repo actually populated that schema from real
`agents/llm/` calls until now (see `docs/LLD/llm-provider.md`'s "per-call latency/cost
interceptors here feed the benchmark's cost/latency metrics").

Design summary
--------------
- **Duration**: measured directly via `time.perf_counter()` wrapped tightly around the single
  `LLMClient.complete_with_usage()` call -- never estimated.
- **Cost**:
  - Ollama (or any provider name in `_FREE_PROVIDERS`, matching
    `cost_latency._FREE_LOCAL_PROVIDERS`'s exact convention): always `$0.0`, unconditionally,
    regardless of whether usage data happens to be available.
  - OpenRouter/Gemini (or any other provider name): computed as
    `prompt_tokens/1000 * rate.prompt_usd_per_1k + completion_tokens/1000 *
    rate.completion_usd_per_1k`, using the *real* token counts the provider reported (via
    `LLMClient.complete_with_usage()`, see `agents/llm/client.py`, `openrouter_client.py`,
    `gemini_client.py`) and a small static per-model rate table. If usage data is unavailable, or
    no rate-table entry exists for the resolved `(provider, model)` pair, this raises
    `LLMInterceptorError` rather than silently guessing a cost or leaving `cost_usd=None` --
    `cost_latency.resolve_cost_usd` already has no fallback beyond Ollama, so a `None` here would
    just surface as a less-informative `ValueError` two layers downstream instead.
- **Rate table**: `DEFAULT_RATE_TABLE` below is an injectable default, not a hardcoded closed
  set -- `LLMInterceptor.__init__(rate_table=...)` accepts a full override. It is deliberately
  small, covering only the two models `agents/llm/openrouter_client.py`'s and
  `agents/llm/gemini_client.py`'s own `DEFAULT_MODEL` constants name today
  (`openai/gpt-4o-mini`, `gemini-2.5-flash`), matching `docs/LLD/llm-provider.md`'s "OpenRouter
  (GPT-4o-mini)" / "Gemini API (2.5/3.5 Flash)" guidance -- **not** an attempt to price every
  model either provider offers. The per-1K-token rates themselves are illustrative, broadly
  realistic example figures for these model tiers as of this module's authorship, explicitly
  **not** a committed pricing contract: real production pricing should be passed in via
  `rate_table=` (e.g. sourced from config), and the actual model choice for judging/benchmarking
  belongs to subtasks 5.3.2/5.3.4, not this one.

No new dependency: only `time`/`dataclasses`/`collections.abc`/`typing` from the standard
library, matching `agents/eval/cost_latency.py`'s own "no new dependency" constraint --
`agents/pyproject.toml` is untouched.
"""

from __future__ import annotations

import time
from collections.abc import Mapping
from dataclasses import dataclass

from eval.cost_latency import StageRecord
from llm.client import CompletionResult, LLMClient, LLMError

#: Providers treated as local/free, unconditionally -- mirrors
#: `cost_latency._FREE_LOCAL_PROVIDERS` exactly (case-insensitive match against the `provider`
#: string passed to `LLMInterceptor.call`).
_FREE_PROVIDERS = frozenset({"ollama"})


@dataclass(frozen=True)
class ModelRate:
    """Per-1,000-token USD pricing for one `(provider, model)` pair.

    Attributes:
        prompt_usd_per_1k: USD cost per 1,000 prompt (input) tokens.
        completion_usd_per_1k: USD cost per 1,000 completion (output) tokens.
    """

    prompt_usd_per_1k: float
    completion_usd_per_1k: float


#: Illustrative default rate table -- see module docstring's "Rate table" section for why this
#: is deliberately small and explicitly not a committed pricing contract. Keyed by lowercase
#: provider name, then the exact model string as it appears in `CompletionResult.model` (i.e.
#: `OpenRouterClient`/`GeminiClient`'s own `DEFAULT_MODEL` constants, or whatever `model=`
#: override a caller resolves to).
DEFAULT_RATE_TABLE: dict[str, dict[str, ModelRate]] = {
    "openrouter": {
        # openai/gpt-4o-mini via OpenRouter -- agents/llm/openrouter_client.py's DEFAULT_MODEL.
        "openai/gpt-4o-mini": ModelRate(
            prompt_usd_per_1k=0.00015, completion_usd_per_1k=0.0006
        ),
    },
    "gemini": {
        # gemini-2.5-flash -- agents/llm/gemini_client.py's DEFAULT_MODEL.
        "gemini-2.5-flash": ModelRate(
            prompt_usd_per_1k=0.000075, completion_usd_per_1k=0.0003
        ),
    },
}


class LLMInterceptorError(LLMError):
    """Raised when a non-free-provider call cannot be priced.

    Two distinct causes, both surfaced as this one exception type (matching
    `cost_latency.resolve_cost_usd`'s own "refuse to invent a price" philosophy):

    - The provider's `CompletionResult` carried no `usage` (e.g. the client hasn't been
      extended to parse it, or the provider's response omitted it).
    - `usage` was present, but no rate-table entry exists for the resolved `(provider, model)`
      pair (e.g. a model not yet added to `DEFAULT_RATE_TABLE` or a caller-supplied
      `rate_table=`).

    Never silently downgrades to `cost_usd=0.0` or `None` for a non-free provider -- doing so
    would just relocate today's `cost_latency.resolve_cost_usd` `ValueError` to a less
    informative point, or (worse) silently under-report cost.
    """


@dataclass(frozen=True)
class InterceptedCompletion:
    """Result of one `LLMInterceptor.call()` invocation.

    Attributes:
        text: The completion text -- identical to what the wrapped client's `complete()` would
            have returned for an equivalent call.
        record: A `cost_latency.StageRecord` describing this call's duration/cost/provider,
            ready to be passed directly into `cost_latency.aggregate_by_stage` /
            `cost_latency.rollup_cost_per_1000_queries` alongside other calls' records, with no
            adaptation layer needed.
    """

    text: str
    record: StageRecord


class LLMInterceptor:
    """Wraps `LLMClient.complete()`-style calls, recording duration + cost as a `StageRecord`.

    Args:
        rate_table: Per-`(provider, model)` USD pricing used to cost paid-provider calls.
            Defaults to `DEFAULT_RATE_TABLE` when omitted (or when passed as `None`). Passed as
            a plain nested `Mapping` so callers can supply their own dict (or any
            `Mapping`-compatible config-derived structure) without needing to construct
            `ModelRate` instances for providers/models they don't use -- only the keys actually
            looked up during a `call()` need to be present.

    Thread-safety: this class holds no mutable per-call state (the rate table is read-only after
        construction), so a single instance may be shared across concurrent callers.
    """

    def __init__(
        self,
        *,
        rate_table: Mapping[str, Mapping[str, ModelRate]] | None = None,
    ) -> None:
        self._rate_table: Mapping[str, Mapping[str, ModelRate]] = (
            rate_table if rate_table is not None else DEFAULT_RATE_TABLE
        )

    def call(
        self,
        client: LLMClient,
        *,
        provider: str,
        arm: str,
        stage: str,
        prompt: str,
        model: str | None = None,
        query_id: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> InterceptedCompletion:
        """Perform one intercepted `complete()`-style call and record it as a `StageRecord`.

        Args:
            client: Any `LLMClient` implementation (`OllamaClient`, `OpenRouterClient`,
                `GeminiClient`, or a future one) to call through.
            provider: Free-form provider label for the resulting `StageRecord.provider` (e.g.
                `"ollama"`, `"openrouter"`, `"gemini"`) -- matched case-insensitively against
                `_FREE_PROVIDERS` to decide the always-`$0.0` path. Not introspected from
                `client` itself, since no `LLMClient` implementation currently self-reports a
                provider name (see `agents/llm/factory.py`); the caller (which chose/constructed
                `client`) already knows this.
            arm: Benchmark arm name for the resulting `StageRecord.arm` (e.g. `"hivemind"`,
                `"vector_rag"`), per `cost_latency.StageRecord`'s own field semantics.
            stage: Pipeline stage name for the resulting `StageRecord.stage` (e.g.
                `"final_answer"`, `"entity_extraction"`).
            prompt: Forwarded unchanged to `client.complete_with_usage(...)`.
            model: Forwarded unchanged to `client.complete_with_usage(...)`; also determines
                which rate-table entry is looked up for paid providers (via the *resolved*
                model name `complete_with_usage` reports back, not this raw override, so a
                `None` here still resolves correctly to the client's own configured default).
            query_id: Forwarded unchanged into the resulting `StageRecord.query_id`.
            temperature: Forwarded unchanged to `client.complete_with_usage(...)`.
            max_tokens: Forwarded unchanged to `client.complete_with_usage(...)`.
            timeout: Forwarded unchanged to `client.complete_with_usage(...)`.

        Returns:
            An `InterceptedCompletion` with the call's completion text and a `StageRecord`
            capturing directly-measured `duration_seconds` and a resolved `cost_usd`
            (`0.0` for `_FREE_PROVIDERS`, computed from real token usage otherwise).

        Raises:
            LLMError: Whatever `client.complete_with_usage()` itself raises (e.g.
                `OpenRouterClientError`, `GeminiClientError`, `OllamaClientError`) on any
                provider call failure, propagated unchanged -- this method does not swallow or
                wrap call failures, only successful-call cost/duration accounting.
            LLMInterceptorError: If `provider` is not a known free provider and its cost cannot
                be determined (see this class's and `LLMInterceptorError`'s docstrings).
        """
        start = time.perf_counter()
        result = client.complete_with_usage(
            prompt,
            model=model,
            temperature=temperature,
            max_tokens=max_tokens,
            timeout=timeout,
        )
        duration_seconds = time.perf_counter() - start

        cost_usd = self._resolve_cost(provider, result)

        record = StageRecord(
            arm=arm,
            stage=stage,
            duration_seconds=duration_seconds,
            provider=provider,
            cost_usd=cost_usd,
            query_id=query_id,
        )
        return InterceptedCompletion(text=result.text, record=record)

    def _resolve_cost(self, provider: str, result: CompletionResult) -> float:
        """Return the USD cost to attribute to one intercepted call.

        See `LLMInterceptorError`'s docstring for the two failure modes on the paid-provider
        path.
        """
        if provider.lower() in _FREE_PROVIDERS:
            return 0.0

        if result.usage is None:
            raise LLMInterceptorError(
                f"Cannot price provider={provider!r} model={result.model!r}: no token usage "
                "was reported for this call (CompletionResult.usage is None). Only free "
                f"providers ({sorted(_FREE_PROVIDERS)!r}) may omit cost data."
            )

        provider_rates = self._rate_table.get(provider.lower())
        rate = provider_rates.get(result.model) if provider_rates is not None else None
        if rate is None:
            raise LLMInterceptorError(
                f"No rate-table entry for provider={provider!r} model={result.model!r}. "
                "Pass a rate_table= covering this (provider, model) pair to LLMInterceptor(); "
                "refusing to invent a per-token price for an uncatalogued model."
            )

        return (
            result.usage.prompt_tokens / 1000 * rate.prompt_usd_per_1k
            + result.usage.completion_tokens / 1000 * rate.completion_usd_per_1k
        )
