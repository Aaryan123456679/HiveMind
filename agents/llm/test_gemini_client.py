"""Tests for `llm.gemini_client.GeminiClient` and the `llm.client.LLMClient`
interface it implements.

Per issue #20 subtask 4.1.2's test spec (as scoped by the implementation
dispatch: "HTTP call mocked" rather than the issue body's "SDK call
mocked", since this client talks Gemini's REST API directly via `httpx`
rather than the `google-generativeai` SDK -- see `gemini_client.py`'s
module docstring): mock the Gemini HTTP call (no real network calls in
this suite) and assert `complete()`'s request shape (endpoint path incl.
model and API key query param, payload) and response parsing (correct
extraction of the completion text from Gemini's `candidates` JSON response
shape), plus API-key resolution and error handling for HTTP failures and
malformed responses.

All HTTP interception uses `httpx.MockTransport` injected via
`GeminiClient(transport=...)` -- no monkeypatching of `httpx` internals
and no real sockets opened.
"""

from __future__ import annotations

import json

import httpx
import pytest

from llm.client import LLMClient, LLMError
from llm.gemini_client import (
    API_KEY_ENV_VAR,
    DEFAULT_BASE_URL,
    DEFAULT_MODEL,
    GeminiClient,
    GeminiClientError,
)

_TEST_API_KEY = "test-api-key-123"

_OK_RESPONSE_JSON = {
    "candidates": [{"content": {"parts": [{"text": "ok"}]}}]
}


def _client_with_handler(handler, **kwargs) -> GeminiClient:
    kwargs.setdefault("api_key", _TEST_API_KEY)
    return GeminiClient(transport=httpx.MockTransport(handler), **kwargs)


# ---------------------------------------------------------------------------
# LLMClient interface contract
# ---------------------------------------------------------------------------


def test_llmclient_is_abstract_and_cannot_be_instantiated() -> None:
    with pytest.raises(TypeError):
        LLMClient()  # type: ignore[abstract]


def test_gemini_client_is_instance_of_llmclient() -> None:
    client = GeminiClient(api_key=_TEST_API_KEY)
    assert isinstance(client, LLMClient)


# ---------------------------------------------------------------------------
# API key resolution
# ---------------------------------------------------------------------------


def test_api_key_from_explicit_kwarg() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.params["key"] == _TEST_API_KEY
        return httpx.Response(200, json=_OK_RESPONSE_JSON)

    client = _client_with_handler(handler, api_key=_TEST_API_KEY)
    client.complete("hi")


def test_api_key_falls_back_to_env_var(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(API_KEY_ENV_VAR, "env-key-456")
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(200, json=_OK_RESPONSE_JSON)

    client = GeminiClient(transport=httpx.MockTransport(handler))
    client.complete("hi")

    assert seen_requests[0].url.params["key"] == "env-key-456"


def test_missing_api_key_raises_at_construction(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv(API_KEY_ENV_VAR, raising=False)
    with pytest.raises(GeminiClientError):
        GeminiClient()


# ---------------------------------------------------------------------------
# Request shape
# ---------------------------------------------------------------------------


def test_complete_posts_to_generate_content_endpoint() -> None:
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(200, json=_OK_RESPONSE_JSON)

    client = _client_with_handler(handler)
    client.complete("hello")

    assert len(seen_requests) == 1
    request = seen_requests[0]
    assert request.method == "POST"
    assert request.url.path == f"/v1beta/models/{DEFAULT_MODEL}:generateContent"
    assert request.url.params["key"] == _TEST_API_KEY


def test_complete_request_payload_shape() -> None:
    seen_bodies = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_bodies.append(json.loads(request.content))
        return httpx.Response(200, json=_OK_RESPONSE_JSON)

    client = _client_with_handler(handler, model="custom-model")
    client.complete("summarize this document", temperature=0.2, max_tokens=256)

    assert len(seen_bodies) == 1
    body = seen_bodies[0]
    assert body["contents"] == [
        {"parts": [{"text": "summarize this document"}]}
    ]
    assert body["generationConfig"]["temperature"] == 0.2
    assert body["generationConfig"]["maxOutputTokens"] == 256


def test_complete_omits_max_output_tokens_when_not_given() -> None:
    seen_bodies = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_bodies.append(json.loads(request.content))
        return httpx.Response(200, json=_OK_RESPONSE_JSON)

    client = _client_with_handler(handler)
    client.complete("hi")

    assert "maxOutputTokens" not in seen_bodies[0]["generationConfig"]


def test_complete_uses_configured_base_url_and_default_model() -> None:
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(200, json=_OK_RESPONSE_JSON)

    client = _client_with_handler(handler)
    client.complete("hi")

    assert seen_requests[0].url.path == (
        f"/v1beta/models/{DEFAULT_MODEL}:generateContent"
    )
    assert str(seen_requests[0].url).startswith(DEFAULT_BASE_URL)


def test_complete_model_override_per_call() -> None:
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(200, json=_OK_RESPONSE_JSON)

    client = _client_with_handler(handler, model="default-model")
    client.complete("hi", model="override-model")

    assert seen_requests[0].url.path == "/v1beta/models/override-model:generateContent"


def test_default_base_url_and_model_constants() -> None:
    assert DEFAULT_BASE_URL == "https://generativelanguage.googleapis.com/v1beta"
    assert DEFAULT_MODEL == "gemini-2.5-flash"


# ---------------------------------------------------------------------------
# Response parsing
# ---------------------------------------------------------------------------


def test_complete_parses_response_text() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "candidates": [
                    {
                        "content": {
                            "parts": [
                                {"text": "The capital of France is Paris."}
                            ],
                            "role": "model",
                        }
                    }
                ]
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
    with pytest.raises(GeminiClientError):
        client.complete("hi")


def test_complete_raises_on_malformed_json() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=b"not json{{{")

    client = _client_with_handler(handler)
    with pytest.raises(GeminiClientError):
        client.complete("hi")


def test_complete_raises_on_missing_candidates_key() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"modelVersion": DEFAULT_MODEL})

    client = _client_with_handler(handler)
    with pytest.raises(GeminiClientError):
        client.complete("hi")


def test_complete_raises_on_empty_candidates_list() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"candidates": []})

    client = _client_with_handler(handler)
    with pytest.raises(GeminiClientError):
        client.complete("hi")


def test_complete_raises_on_missing_content_text() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"candidates": [{"content": {}}]})

    client = _client_with_handler(handler)
    with pytest.raises(GeminiClientError):
        client.complete("hi")


def test_complete_raises_on_connection_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused", request=request)

    client = _client_with_handler(handler)
    with pytest.raises(GeminiClientError):
        client.complete("hi")


def test_gemini_client_error_is_llm_error() -> None:
    assert issubclass(GeminiClientError, LLMError)
