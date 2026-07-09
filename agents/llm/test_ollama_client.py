"""Tests for `llm.ollama_client.OllamaClient` and the `llm.client.LLMClient`
interface it implements.

Per issue #18 subtask 3.4.1's test spec: mock the Ollama HTTP call (no real
network calls in this suite) and assert `complete()`'s request shape
(endpoint, payload) and response parsing (correct extraction of the
completion text from Ollama's JSON response shape), plus error handling
for HTTP failures and malformed responses.

All HTTP interception uses `httpx.MockTransport` injected via
`OllamaClient(transport=...)` -- no monkeypatching of `httpx` internals and
no real sockets opened.
"""

from __future__ import annotations

import json

import httpx
import pytest

from llm.client import LLMClient
from llm.ollama_client import (
    DEFAULT_BASE_URL,
    DEFAULT_MODEL,
    OllamaClient,
    OllamaClientError,
)


def _client_with_handler(handler, **kwargs) -> OllamaClient:
    return OllamaClient(transport=httpx.MockTransport(handler), **kwargs)


# ---------------------------------------------------------------------------
# LLMClient interface contract
# ---------------------------------------------------------------------------


def test_llmclient_is_abstract_and_cannot_be_instantiated() -> None:
    with pytest.raises(TypeError):
        LLMClient()  # type: ignore[abstract]


def test_ollama_client_is_instance_of_llmclient() -> None:
    client = OllamaClient()
    assert isinstance(client, LLMClient)


# ---------------------------------------------------------------------------
# Request shape
# ---------------------------------------------------------------------------


def test_complete_posts_to_api_generate_endpoint() -> None:
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(200, json={"response": "ok"})

    client = _client_with_handler(handler)
    client.complete("hello")

    assert len(seen_requests) == 1
    request = seen_requests[0]
    assert request.method == "POST"
    assert request.url.path == "/api/generate"


def test_complete_request_payload_shape() -> None:
    seen_bodies = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_bodies.append(json.loads(request.content))
        return httpx.Response(200, json={"response": "ok"})

    client = _client_with_handler(handler, model="custom-model:latest")
    client.complete("summarize this document", temperature=0.2, max_tokens=256)

    assert len(seen_bodies) == 1
    body = seen_bodies[0]
    assert body["model"] == "custom-model:latest"
    assert body["prompt"] == "summarize this document"
    assert body["stream"] is False
    assert body["options"]["temperature"] == 0.2
    assert body["options"]["num_predict"] == 256


def test_complete_uses_configured_base_url_and_default_model() -> None:
    seen_requests = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_requests.append(request)
        return httpx.Response(200, json={"response": "ok"})

    client = _client_with_handler(
        handler, base_url="http://example-ollama-host:11434"
    )
    client.complete("hi")

    body = json.loads(seen_requests[0].content)
    assert body["model"] == DEFAULT_MODEL
    assert str(seen_requests[0].url).startswith(
        "http://example-ollama-host:11434"
    )


def test_complete_model_override_per_call() -> None:
    seen_bodies = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen_bodies.append(json.loads(request.content))
        return httpx.Response(200, json={"response": "ok"})

    client = _client_with_handler(handler, model="default-model")
    client.complete("hi", model="override-model")

    assert seen_bodies[0]["model"] == "override-model"


def test_default_base_url_constant_is_localhost() -> None:
    assert DEFAULT_BASE_URL == "http://localhost:11434"


# ---------------------------------------------------------------------------
# Response parsing
# ---------------------------------------------------------------------------


def test_complete_parses_response_text() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "model": DEFAULT_MODEL,
                "response": "The capital of France is Paris.",
                "done": True,
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
    with pytest.raises(OllamaClientError):
        client.complete("hi")


def test_complete_raises_on_malformed_json() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=b"not json{{{")

    client = _client_with_handler(handler)
    with pytest.raises(OllamaClientError):
        client.complete("hi")


def test_complete_raises_on_missing_response_key() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"model": DEFAULT_MODEL, "done": True})

    client = _client_with_handler(handler)
    with pytest.raises(OllamaClientError):
        client.complete("hi")


def test_complete_raises_on_connection_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused", request=request)

    client = _client_with_handler(handler)
    with pytest.raises(OllamaClientError):
        client.complete("hi")


def test_ollama_client_error_is_llm_error() -> None:
    from llm.client import LLMError

    assert issubclass(OllamaClientError, LLMError)
