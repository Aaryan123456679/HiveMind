"""Tests for `llm.interceptor.LLMInterceptor` (issue #59, subtask 4.5.19.1).

Per the subtask's test spec: mocked provider responses (including realistic token-usage fields)
assert correct duration measurement, correct `cost_usd` computation per provider/model, and
correct `$0.0` for Ollama; an integration-style test constructs a few `StageRecord`s from
interceptor output and feeds them through `agents/eval/cost_latency.py`'s existing functions to
confirm compatibility.

All HTTP interception uses `httpx.MockTransport`, matching every other `agents/llm/` test file's
convention -- no real network calls anywhere in this suite.
"""

from __future__ import annotations

import time

import httpx
import pytest

from eval.cost_latency import aggregate_by_stage, rollup_cost_per_1000_queries
from llm.gemini_client import GeminiClient
from llm.interceptor import (
    DEFAULT_RATE_TABLE,
    InterceptedCompletion,
    LLMInterceptor,
    LLMInterceptorError,
    ModelRate,
)
from llm.ollama_client import OllamaClient
from llm.openrouter_client import OpenRouterClient

_TEST_API_KEY = "test-api-key-123"


def _ollama_client(handler) -> OllamaClient:
    return OllamaClient(transport=httpx.MockTransport(handler))


def _openrouter_client(handler, **kwargs) -> OpenRouterClient:
    kwargs.setdefault("api_key", _TEST_API_KEY)
    return OpenRouterClient(transport=httpx.MockTransport(handler), **kwargs)


def _gemini_client(handler, **kwargs) -> GeminiClient:
    kwargs.setdefault("api_key", _TEST_API_KEY)
    return GeminiClient(transport=httpx.MockTransport(handler), **kwargs)


# ---------------------------------------------------------------------------
# Duration measurement
# ---------------------------------------------------------------------------


def test_duration_reflects_actual_call_time() -> None:
    """duration_seconds must be measured directly, not zero/estimated."""
    _SLEEP_SECONDS = 0.05

    def handler(request: httpx.Request) -> httpx.Response:
        time.sleep(_SLEEP_SECONDS)
        return httpx.Response(200, json={"response": "ok"})

    client = _ollama_client(handler)
    interceptor = LLMInterceptor()

    result = interceptor.call(
        client, provider="ollama", arm="hivemind", stage="final_answer", prompt="hi"
    )

    assert result.record.duration_seconds >= _SLEEP_SECONDS
    # Sanity upper bound -- guards against e.g. accidentally measuring wall-clock across the
    # whole test session instead of just this one call.
    assert result.record.duration_seconds < _SLEEP_SECONDS + 5.0


def test_returns_intercepted_completion_with_matching_text() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"response": "the answer"})

    client = _ollama_client(handler)
    interceptor = LLMInterceptor()

    result = interceptor.call(
        client, provider="ollama", arm="hivemind", stage="final_answer", prompt="hi"
    )

    assert isinstance(result, InterceptedCompletion)
    assert result.text == "the answer"


# ---------------------------------------------------------------------------
# Ollama: cost_usd always $0.0
# ---------------------------------------------------------------------------


def test_ollama_call_cost_is_always_zero() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"response": "ok"})

    client = _ollama_client(handler)
    interceptor = LLMInterceptor()

    result = interceptor.call(
        client, provider="ollama", arm="hivemind", stage="final_answer", prompt="hi"
    )

    assert result.record.cost_usd == 0.0
    assert result.record.provider == "ollama"


def test_ollama_call_cost_zero_case_insensitive_provider_label() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"response": "ok"})

    client = _ollama_client(handler)
    interceptor = LLMInterceptor()

    result = interceptor.call(
        client, provider="Ollama", arm="hivemind", stage="final_answer", prompt="hi"
    )

    assert result.record.cost_usd == 0.0


# ---------------------------------------------------------------------------
# OpenRouter: cost_usd computed from real token usage x rate table
# ---------------------------------------------------------------------------


def test_openrouter_cost_computed_from_usage_and_rate_table() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "choices": [{"message": {"content": "the answer"}}],
                "usage": {
                    "prompt_tokens": 1000,
                    "completion_tokens": 500,
                    "total_tokens": 1500,
                },
            },
        )

    client = _openrouter_client(handler)  # DEFAULT_MODEL == "openai/gpt-4o-mini"
    interceptor = LLMInterceptor()

    result = interceptor.call(
        client,
        provider="openrouter",
        arm="vector_rag",
        stage="final_answer",
        prompt="hi",
        query_id="q1",
    )

    rate = DEFAULT_RATE_TABLE["openrouter"]["openai/gpt-4o-mini"]
    expected_cost = 1000 / 1000 * rate.prompt_usd_per_1k + 500 / 1000 * rate.completion_usd_per_1k
    assert result.record.cost_usd == pytest.approx(expected_cost)
    assert result.record.provider == "openrouter"
    assert result.record.query_id == "q1"
    assert result.text == "the answer"


# ---------------------------------------------------------------------------
# Gemini: cost_usd computed from real token usage x rate table
# ---------------------------------------------------------------------------


def test_gemini_cost_computed_from_usage_and_rate_table() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "candidates": [{"content": {"parts": [{"text": "the answer"}]}}],
                "usageMetadata": {
                    "promptTokenCount": 2000,
                    "candidatesTokenCount": 300,
                    "totalTokenCount": 2300,
                },
            },
        )

    client = _gemini_client(handler)  # DEFAULT_MODEL == "gemini-2.5-flash"
    interceptor = LLMInterceptor()

    result = interceptor.call(
        client,
        provider="gemini",
        arm="graphrag_lite",
        stage="final_answer",
        prompt="hi",
    )

    rate = DEFAULT_RATE_TABLE["gemini"]["gemini-2.5-flash"]
    expected_cost = 2000 / 1000 * rate.prompt_usd_per_1k + 300 / 1000 * rate.completion_usd_per_1k
    assert result.record.cost_usd == pytest.approx(expected_cost)
    assert result.record.provider == "gemini"


# ---------------------------------------------------------------------------
# Missing usage / uncatalogued model -> loud failure, never invented pricing
# ---------------------------------------------------------------------------


def test_openrouter_missing_usage_raises() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        # No "usage" key at all in this response.
        return httpx.Response(200, json={"choices": [{"message": {"content": "ok"}}]})

    client = _openrouter_client(handler)
    interceptor = LLMInterceptor()

    with pytest.raises(LLMInterceptorError):
        interceptor.call(
            client, provider="openrouter", arm="vector_rag", stage="final_answer", prompt="hi"
        )


def test_unknown_model_for_paid_provider_raises() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "choices": [{"message": {"content": "ok"}}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 5},
            },
        )

    client = _openrouter_client(handler, model="some/unlisted-model")
    interceptor = LLMInterceptor()

    with pytest.raises(LLMInterceptorError):
        interceptor.call(
            client,
            provider="openrouter",
            arm="vector_rag",
            stage="final_answer",
            prompt="hi",
            model="some/unlisted-model",
        )


# ---------------------------------------------------------------------------
# Rate table is injectable/overridable, not a hardcoded closed set
# ---------------------------------------------------------------------------


def test_custom_rate_table_overrides_default() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "choices": [{"message": {"content": "ok"}}],
                "usage": {"prompt_tokens": 1000, "completion_tokens": 1000},
            },
        )

    client = _openrouter_client(handler, model="custom/cheap-model")
    custom_rate_table = {
        "openrouter": {"custom/cheap-model": ModelRate(prompt_usd_per_1k=1.0, completion_usd_per_1k=2.0)}
    }
    interceptor = LLMInterceptor(rate_table=custom_rate_table)

    result = interceptor.call(
        client,
        provider="openrouter",
        arm="vector_rag",
        stage="final_answer",
        prompt="hi",
        model="custom/cheap-model",
    )

    assert result.record.cost_usd == pytest.approx(1.0 + 2.0)


# ---------------------------------------------------------------------------
# Integration: interceptor output feeds straight into cost_latency, unmodified
# ---------------------------------------------------------------------------


def test_interceptor_records_feed_cost_latency_aggregation() -> None:
    def ollama_handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"response": "ok"})

    def openrouter_handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "choices": [{"message": {"content": "ok"}}],
                "usage": {"prompt_tokens": 1000, "completion_tokens": 500},
            },
        )

    def gemini_handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "candidates": [{"content": {"parts": [{"text": "ok"}]}}],
                "usageMetadata": {"promptTokenCount": 800, "candidatesTokenCount": 200},
            },
        )

    interceptor = LLMInterceptor()

    ollama_result = interceptor.call(
        _ollama_client(ollama_handler),
        provider="ollama",
        arm="hivemind",
        stage="final_answer",
        prompt="q",
        query_id="q1",
    )
    vector_rag_result = interceptor.call(
        _openrouter_client(openrouter_handler),
        provider="openrouter",
        arm="vector_rag",
        stage="final_answer",
        prompt="q",
        query_id="q1",
    )
    vector_rag_embedding_result = interceptor.call(
        _openrouter_client(openrouter_handler),
        provider="openrouter",
        arm="vector_rag",
        stage="embedding",
        prompt="q",
        query_id="q1",
    )
    graphrag_result = interceptor.call(
        _gemini_client(gemini_handler),
        provider="gemini",
        arm="graphrag_lite",
        stage="final_answer",
        prompt="q",
        query_id="q2",
    )

    records = [
        ollama_result.record,
        vector_rag_result.record,
        vector_rag_embedding_result.record,
        graphrag_result.record,
    ]

    # No modification, no adaptation layer: these are the real cost_latency.py functions from
    # subtask 5.3.3, called directly on interceptor output.
    aggregates = aggregate_by_stage(records)
    assert {(a.arm, a.stage) for a in aggregates} == {
        ("hivemind", "final_answer"),
        ("vector_rag", "final_answer"),
        ("vector_rag", "embedding"),
        ("graphrag_lite", "final_answer"),
    }
    hivemind_final = next(a for a in aggregates if a.arm == "hivemind" and a.stage == "final_answer")
    assert hivemind_final.total_cost_usd == 0.0
    assert hivemind_final.call_count == 1

    summaries = rollup_cost_per_1000_queries(records)
    arms = {s.arm for s in summaries}
    assert arms == {"hivemind", "vector_rag", "graphrag_lite"}
    vector_rag_summary = next(s for s in summaries if s.arm == "vector_rag")
    # vector_rag has 2 stage records but both tagged query_id="q1" -> 1 distinct query.
    assert vector_rag_summary.query_count == 1
    assert vector_rag_summary.total_cost_usd > 0.0
