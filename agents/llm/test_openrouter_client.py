"""Tests for `llm.openrouter_client.OpenRouterClient` and the
`llm.client.LLMClient` interface it implements.

Per issue #20 subtask 4.1.1's test spec: mock the OpenRouter HTTP call (no
real network calls in this suite) and assert `complete()`'s request shape
(endpoint, payload, auth header) and response parsing (correct extraction
of the completion text from OpenRouter's OpenAI-compatible JSON response
shape), plus API-key resolution and error handling for HTTP failures and
malformed responses.

All HTTP interception uses `httpx.MockTransport` injected via
`OpenRouterClient(transport=...)` -- no monkeypatching of `httpx`
internals and no real sockets opened.
"""

from __future__ import annotations

import json

import httpx
import pytest

from llm.client import LLMClient, LLMError
from llm.openrouter_client import (
    API_KEY_ENV_VAR,
    DEFAULT_BASE_URL,
    DEFAULT_MODEL,
    OpenRouterClient,
    OpenRouterClientError,
)

_TEST_API_KEY = "test-api-key-123"


def _client_with_handler(handler, **kwargs) -> OpenRouterClient:
    kwargs.setdefault("api_key", _TEST_API_KEY)
    return OpenRouterClient(transport=httpx.MockTransport(handler), **kwargs)


# ---------------------------------------------------------------------------
# LLMClient interface contract
# ---------------------------------------------------------------------------


def test_llmclient_is_abstract_and_cannot_be_instantiated() -> None:
    with pytest.raises(TypeError):
        LLMClient()  # type: ignore[abstract]


def test_openrouter_client_is_instance_of_llmclient() -> None:
    client = OpenRouterClient(api_key=_TEST_API_KEY)
    assert isinstance(client, LLMClient)


# ---------------------------------------------------------------------------
# API key resolution
# ---------------------------------------------------------------------------


def test_api_key_from_explicit_kwarg() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.headers["authorization"] == f"Bearer {_TEST_API_KEY}"
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = _client_with_handler(handler, api_key=_TEST_API_KEY)
    client.complete("hi")


def test_api_key_falls_back_to_env_var(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(API_KEY_ENV_VAR, "env-key-456")
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = OpenRouterClient(transport=httpx.MockTransport(handler))
    client.complete("hi")

    assert seen_requests[0].headers["authorization"] == "Bearer env-key-456"


def test_missing_api_key_raises_at_construction(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv(API_KEY_ENV_VAR, raising=False)
    with pytest.raises(OpenRouterClientError):
        OpenRouterClient()


# ---------------------------------------------------------------------------
# Request shape
# ---------------------------------------------------------------------------


def test_complete_posts_to_chat_completions_endpoint() -> None:
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = _client_with_handler(handler)
    client.complete("hello")

    assert len(seen_requests) == 1
    request = seen_requests[0]
    assert request.method == "POST"
    assert request.url.path == "/api/v1/chat/completions"


def test_complete_request_payload_shape() -> None:
    seen_bodies = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_bodies.append(json.loads(request.content))
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = _client_with_handler(handler, model="custom/model")
    client.complete("summarize this document", temperature=0.2, max_tokens=256)

    assert len(seen_bodies) == 1
    body = seen_bodies[0]
    assert body["model"] == "custom/model"
    assert body["messages"] == [
        {"role": "user", "content": "summarize this document"}
    ]
    assert body["temperature"] == 0.2
    assert body["max_tokens"] == 256


def test_complete_omits_max_tokens_when_not_given() -> None:
    seen_bodies = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_bodies.append(json.loads(request.content))
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = _client_with_handler(handler)
    client.complete("hi")

    assert "max_tokens" not in seen_bodies[0]


def test_complete_sends_authorization_header() -> None:
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = _client_with_handler(handler)
    client.complete("hi")

    assert (
        seen_requests[0].headers["authorization"] == f"Bearer {_TEST_API_KEY}"
    )


def test_complete_uses_configured_base_url_and_default_model() -> None:
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = _client_with_handler(handler)
    client.complete("hi")

    body = json.loads(seen_requests[0].content)
    assert body["model"] == DEFAULT_MODEL
    assert str(seen_requests[0].url).startswith(DEFAULT_BASE_URL)


def test_complete_model_override_per_call() -> None:
    seen_bodies = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_bodies.append(json.loads(request.content))
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = _client_with_handler(handler, model="default/model")
    client.complete("hi", model="override/model")

    assert seen_bodies[0]["model"] == "override/model"


def test_default_base_url_and_model_constants() -> None:
    assert DEFAULT_BASE_URL == "https://openrouter.ai/api/v1"
    assert DEFAULT_MODEL == "openai/gpt-4o-mini"


# ---------------------------------------------------------------------------
# Response parsing
# ---------------------------------------------------------------------------


def test_complete_parses_response_text() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "model": DEFAULT_MODEL,
                "choices": [
                    {
                        "message": {
                            "role": "assistant",
                            "content": "The capital of France is Paris.",
                        }
                    }
                ],
            },
        )

    client = _client_with_handler(handler)
    result = client.complete("What is the capital of France?")

    assert result == "The capital of France is Paris."


# ---------------------------------------------------------------------------
# Error handling -- must raise, never silently swallow
# ---------------------------------------------------------------------------


def test_complete_raises_on_http_error_status() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, text="internal server error")

    client = _client_with_handler(handler)
    with pytest.raises(OpenRouterClientError):
        client.complete("hi")


def test_complete_raises_on_malformed_json() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=b"not json{{{")

    client = _client_with_handler(handler)
    with pytest.raises(OpenRouterClientError):
        client.complete("hi")


def test_complete_raises_on_missing_choices_key() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"model": DEFAULT_MODEL})

    client = _client_with_handler(handler)
    with pytest.raises(OpenRouterClientError):
        client.complete("hi")


def test_complete_raises_on_empty_choices_list() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"choices": []})

    client = _client_with_handler(handler)
    with pytest.raises(OpenRouterClientError):
        client.complete("hi")


def test_complete_raises_on_missing_message_content() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"choices": [{"message": {}}]})

    client = _client_with_handler(handler)
    with pytest.raises(OpenRouterClientError):
        client.complete("hi")


def test_complete_raises_on_connection_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused", request=request)

    client = _client_with_handler(handler)
    with pytest.raises(OpenRouterClientError):
        client.complete("hi")


def test_complete_raises_on_timeout() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.TimeoutException("request timed out", request=request)

    client = _client_with_handler(handler)
    with pytest.raises(OpenRouterClientError):
        client.complete("hi")


def test_openrouter_client_error_is_llm_error() -> None:
    assert issubclass(OpenRouterClientError, LLMError)


# ---------------------------------------------------------------------------
# Persistent client reuse (issue #54 subtask 4.5.16.2)
# ---------------------------------------------------------------------------


def test_complete_client_reuse_across_calls() -> None:
    """`complete()` must reuse one persistent `httpx.Client`/transport for
    the instance's lifetime rather than opening a fresh one per call."""

    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = _client_with_handler(handler)
    try:
        client_before_first_call = client._client
        client.complete("first call")
        client_after_first_call = client._client

        client.complete("second call")
        client_after_second_call = client._client

        assert client_before_first_call is client_after_first_call
        assert client_after_first_call is client_after_second_call
    finally:
        client.close()


def test_client_can_be_used_as_context_manager() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    with _client_with_handler(handler) as client:
        result = client.complete("hi")

    assert result == "ok"
    assert client._client.is_closed


def test_close_closes_underlying_client() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200, json={"choices": [{"message": {"content": "ok"}}]}
        )

    client = _client_with_handler(handler)
    assert not client._client.is_closed

    client.close()

    assert client._client.is_closed
