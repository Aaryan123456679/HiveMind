"""`OllamaClient`: `LLMClient` implementation backed by a local Ollama server.

Per issue #18 subtask 3.4.1. Talks HTTP to Ollama's `/api/generate`
endpoint; this is the *only* module in `agents/` permitted to do so (see
`agents/llm/client.py` and `docs/LLD/llm-provider.md`).

Endpoint choice -- disclosed design
------------------------------------
Ollama exposes both `/api/generate` (single prompt in, single completion
out) and `/api/chat` (multi-turn message list). `complete()`'s contract is
single-shot "prompt in, text out" with no conversation state, so
`/api/generate` is used: its request/response shape maps directly onto
`complete()` without needing to wrap the prompt in a messages list for no
benefit.

Default model -- disclosed choice
------------------------------------
`DEFAULT_MODEL = "llama3.1:8b"` -- the Ollama model-library tag for Llama
3.1 8B (as pulled via `ollama pull llama3.1:8b`), matching the issue body
and `docs/LLD/llm-provider.md`'s "default e.g. Llama 3.1 8B, chosen for
cost at high call volume" guidance for ingestion-time segmentation calls.

Error handling -- disclosed design
------------------------------------
Any HTTP-level failure (connection error, timeout, non-2xx status) and any
response-parsing failure (non-JSON body, JSON body missing the expected
`"response"` key) raises `OllamaClientError`. Nothing is silently
swallowed or converted into an empty-string/None result.
"""

from __future__ import annotations

import httpx

from llm.client import LLMClient, LLMError

#: Ollama's standard local-server default address.
DEFAULT_BASE_URL = "http://localhost:11434"

#: Ollama model-library tag for Llama 3.1 8B; see module docstring.
DEFAULT_MODEL = "llama3.1:8b"

#: Local 8B-class generation on CPU can be slow; generous default timeout.
DEFAULT_TIMEOUT_SECONDS = 120.0


class OllamaClientError(LLMError):
    """Raised on any Ollama HTTP call failure or malformed response."""


class OllamaClient(LLMClient):
    """`LLMClient` implementation that calls a local Ollama server.

    Args:
        base_url: Ollama server base URL. Defaults to
            `DEFAULT_BASE_URL` (Ollama's standard local address).
        model: Default model tag used when a call does not override it
            via `complete(..., model=...)`. Defaults to `DEFAULT_MODEL`.
        timeout: Default per-call timeout in seconds, used when a call
            does not override it via `complete(..., timeout=...)`.
        transport: Optional `httpx.BaseTransport` override, used by tests
            to inject `httpx.MockTransport` and avoid any real network
            call. Production callers should leave this `None`.
    """

    def __init__(
        self,
        *,
        base_url: str = DEFAULT_BASE_URL,
        model: str = DEFAULT_MODEL,
        timeout: float = DEFAULT_TIMEOUT_SECONDS,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._timeout = timeout
        self._transport = transport

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        """See `LLMClient.complete`. Calls Ollama's `/api/generate`."""
        options: dict[str, object] = {"temperature": temperature}
        if max_tokens is not None:
            options["num_predict"] = max_tokens

        payload = {
            "model": model or self._model,
            "prompt": prompt,
            "stream": False,
            "options": options,
        }

        request_timeout = timeout if timeout is not None else self._timeout

        try:
            with httpx.Client(
                base_url=self._base_url, transport=self._transport
            ) as client:
                response = client.post(
                    "/api/generate", json=payload, timeout=request_timeout
                )
                response.raise_for_status()
        except httpx.HTTPError as exc:
            raise OllamaClientError(
                f"Ollama request to {self._base_url}/api/generate failed: {exc}"
            ) from exc

        try:
            data = response.json()
        except ValueError as exc:
            raise OllamaClientError(
                f"Ollama response was not valid JSON: {exc}"
            ) from exc

        if not isinstance(data, dict) or "response" not in data:
            raise OllamaClientError(
                f"Ollama response missing expected 'response' key: {data!r}"
            )

        completion = data["response"]
        if not isinstance(completion, str):
            raise OllamaClientError(
                f"Ollama response's 'response' field was not a string: {completion!r}"
            )

        return completion
