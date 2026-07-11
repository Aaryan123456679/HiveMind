"""`OpenRouterClient`: `LLMClient` implementation backed by OpenRouter.

Per issue #20 subtask 4.1.1. Talks HTTP to OpenRouter's
`/chat/completions` endpoint (OpenAI-Chat-Completions-compatible); this is
one of the modules in `agents/llm/` permitted to call a provider API
directly (see `agents/llm/client.py` and `docs/LLD/llm-provider.md`).

Endpoint choice -- disclosed design
------------------------------------
OpenRouter exposes an OpenAI-compatible `/chat/completions` endpoint,
which expects a `messages` list rather than a single `prompt` string.
`complete()`'s contract is single-shot "prompt in, text out" with no
conversation state, so the prompt is wrapped in a single `{"role":
"user", "content": prompt}` message -- the same mapping strategy
`OllamaClient` uses when it puts `complete()`'s single prompt string
directly into Ollama's single-prompt `/api/generate` field, just applied
to a chat-shaped endpoint since OpenRouter has no non-chat completion
endpoint.

Default model -- disclosed choice
------------------------------------
`DEFAULT_MODEL = "openai/gpt-4o-mini"` -- OpenRouter's `<provider>/<model>`
model-slug convention for GPT-4o-mini, matching the issue body and
`docs/LLD/llm-provider.md`'s "OpenRouter (GPT-4o-mini) -- used for
query-time agents" guidance.

API key handling -- disclosed design
--------------------------------------
Unlike local Ollama, OpenRouter requires bearer-token auth on every
request. `api_key` may be passed explicitly (mainly for tests); if not
given, it falls back to the `OPENROUTER_API_KEY` environment variable. If
neither is available, construction fails immediately with
`OpenRouterClientError` -- never silently sending an unauthenticated
request that would only fail later inside `complete()`.

Error handling -- disclosed design
------------------------------------
Any HTTP-level failure (connection error, timeout, non-2xx status) and
any response-parsing failure (non-JSON body, JSON body missing the
expected `choices[0].message.content` path) raises
`OpenRouterClientError`. Nothing is silently swallowed or converted into
an empty-string/None result.
"""

from __future__ import annotations

import os

import httpx

from llm.client import CompletionResult, LLMClient, LLMError, TokenUsage

#: OpenRouter's OpenAI-compatible API base URL.
DEFAULT_BASE_URL = "https://openrouter.ai/api/v1"

#: OpenRouter model slug for GPT-4o-mini; see module docstring.
DEFAULT_MODEL = "openai/gpt-4o-mini"

#: Environment variable used as a fallback when `api_key` is not passed
#: explicitly to the constructor.
API_KEY_ENV_VAR = "OPENROUTER_API_KEY"

#: Hosted API over the network; shorter default than Ollama's local-CPU
#: timeout since there is no local model-loading/inference latency here.
DEFAULT_TIMEOUT_SECONDS = 60.0


class OpenRouterClientError(LLMError):
    """Raised on any OpenRouter HTTP call failure or malformed response.

    Also raised at construction time if no API key can be resolved (see
    module docstring's "API key handling" section).
    """


class OpenRouterClient(LLMClient):
    """`LLMClient` implementation that calls the OpenRouter API.

    Args:
        api_key: OpenRouter API key. Defaults to `None`, in which case the
            `OPENROUTER_API_KEY` environment variable is used. Raises
            `OpenRouterClientError` immediately if neither is available.
        base_url: OpenRouter API base URL. Defaults to
            `DEFAULT_BASE_URL`.
        model: Default model slug used when a call does not override it
            via `complete(..., model=...)`. Defaults to `DEFAULT_MODEL`.
        timeout: Default per-call timeout in seconds, used when a call
            does not override it via `complete(..., timeout=...)`.
        transport: Optional `httpx.BaseTransport` override, used by tests
            to inject `httpx.MockTransport` and avoid any real network
            call. Production callers should leave this `None`.

    Client lifecycle -- disclosed design
    --------------------------------------
    A single `httpx.Client` is opened once, in `__init__`, and reused for
    every `complete()` call made on this instance (rather than opening and
    closing a fresh `httpx.Client`, and its connection pool, on every
    call) -- this avoids re-paying TCP/TLS connection-setup cost on every
    hosted-network-provider call. Because the client is now held for the
    instance's lifetime instead of being closed automatically after each
    call, callers that want deterministic cleanup should either call
    `close()` explicitly or use this class as a context manager (`with
    OpenRouterClient(...) as client: ...`), which calls `close()` on
    exit. Not calling `close()` is not a hard error -- `httpx.Client`
    itself only holds idle keep-alive connections, no OS resources
    require synchronous cleanup -- but explicit closing is recommended
    for any code that creates short-lived instances.
    """

    def __init__(
        self,
        *,
        api_key: str | None = None,
        base_url: str = DEFAULT_BASE_URL,
        model: str = DEFAULT_MODEL,
        timeout: float = DEFAULT_TIMEOUT_SECONDS,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        resolved_key = api_key if api_key is not None else os.environ.get(
            API_KEY_ENV_VAR
        )
        if not resolved_key:
            raise OpenRouterClientError(
                "No OpenRouter API key available: pass api_key= explicitly "
                f"or set the {API_KEY_ENV_VAR} environment variable."
            )

        self._api_key = resolved_key
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._timeout = timeout
        self._transport = transport
        # Persistent client, reused across complete() calls -- see the
        # "Client lifecycle" section of this class's docstring.
        self._client = httpx.Client(
            base_url=self._base_url, transport=self._transport
        )

    def close(self) -> None:
        """Close the persistent underlying `httpx.Client`.

        Safe to call multiple times (idempotent, per `httpx.Client.close`).
        Not calling this is not a correctness bug, only a missed cleanup
        opportunity -- see this class's docstring.
        """
        self._client.close()

    def __enter__(self) -> "OpenRouterClient":
        return self

    def __exit__(self, *exc_info: object) -> None:
        self.close()

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        """See `LLMClient.complete`. Calls OpenRouter's `/chat/completions`."""
        return self._do_complete(
            prompt,
            model=model,
            temperature=temperature,
            max_tokens=max_tokens,
            timeout=timeout,
        ).text

    def complete_with_usage(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> CompletionResult:
        """See `LLMClient.complete_with_usage`.

        Added for subtask 4.5.19.1 (issue #59): OpenRouter's OpenAI-compatible response already
        includes a top-level `usage: {prompt_tokens, completion_tokens, total_tokens}` object,
        a sibling of `choices` in the same response body -- previously parsed out of the JSON
        response and then discarded entirely by `complete()`. This method surfaces it via
        `TokenUsage` for `agents/llm/interceptor.py`'s cost computation, without changing
        `complete()`'s own return type or behavior at all (both methods share the same
        underlying `_do_complete()` implementation below).
        """
        return self._do_complete(
            prompt,
            model=model,
            temperature=temperature,
            max_tokens=max_tokens,
            timeout=timeout,
        )

    def _do_complete(
        self,
        prompt: str,
        *,
        model: str | None,
        temperature: float,
        max_tokens: int | None,
        timeout: float | None,
    ) -> CompletionResult:
        """Shared implementation backing both `complete()` and `complete_with_usage()`.

        Extracted for subtask 4.5.19.1 (issue #59) so token-usage parsing (added below) lives in
        exactly one place rather than being duplicated across two near-identical methods.
        Request/response handling, error types, and exception points are unchanged from the
        original `complete()` body this was extracted from.
        """
        resolved_model = model or self._model
        payload: dict[str, object] = {
            "model": resolved_model,
            "messages": [{"role": "user", "content": prompt}],
            "temperature": temperature,
        }
        if max_tokens is not None:
            payload["max_tokens"] = max_tokens

        request_timeout = timeout if timeout is not None else self._timeout

        try:
            response = self._client.post(
                "/chat/completions",
                json=payload,
                headers={"Authorization": f"Bearer {self._api_key}"},
                timeout=request_timeout,
            )
            response.raise_for_status()
        except httpx.HTTPError as exc:
            raise OpenRouterClientError(
                f"OpenRouter request to {self._base_url}/chat/completions "
                f"failed: {exc}"
            ) from exc

        try:
            data = response.json()
        except ValueError as exc:
            raise OpenRouterClientError(
                f"OpenRouter response was not valid JSON: {exc}"
            ) from exc

        if not isinstance(data, dict):
            raise OpenRouterClientError(
                f"OpenRouter response was not a JSON object: {data!r}"
            )

        choices = data.get("choices")
        if not isinstance(choices, list) or not choices:
            raise OpenRouterClientError(
                f"OpenRouter response missing expected 'choices' list: {data!r}"
            )

        message = choices[0].get("message") if isinstance(choices[0], dict) else None
        completion = message.get("content") if isinstance(message, dict) else None

        if not isinstance(completion, str):
            raise OpenRouterClientError(
                "OpenRouter response missing expected "
                f"'choices[0].message.content' string: {data!r}"
            )

        return CompletionResult(
            text=completion,
            model=resolved_model,
            usage=_parse_usage(data.get("usage")),
        )


def _parse_usage(usage: object) -> TokenUsage | None:
    """Best-effort parse of OpenRouter's OpenAI-compatible `usage` object.

    Added for subtask 4.5.19.1 (issue #59). Unlike `completion` text parsing above (which raises
    `OpenRouterClientError` on any malformed shape, since a completion with no text is useless),
    a missing or malformed `usage` object degrades gracefully to `None` -- `usage` is a bonus
    field for cost accounting, not part of `complete()`'s original contract, so a provider
    response that omits or malforms it should not break completion itself. Callers needing usage
    (`agents/llm/interceptor.py`) are responsible for handling a `None` result (e.g. refusing to
    price a paid-provider call with no usage data).
    """
    if not isinstance(usage, dict):
        return None
    prompt_tokens = usage.get("prompt_tokens")
    completion_tokens = usage.get("completion_tokens")
    if not isinstance(prompt_tokens, int) or not isinstance(completion_tokens, int):
        return None
    return TokenUsage(prompt_tokens=prompt_tokens, completion_tokens=completion_tokens)
