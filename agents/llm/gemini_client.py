"""`GeminiClient`: `LLMClient` implementation backed by the Gemini API.

Per issue #20 subtask 4.1.2. Talks HTTP to Gemini's Generative Language REST
API `models/{model}:generateContent` endpoint; this is one of the modules in
`agents/llm/` permitted to call a provider API directly (see
`agents/llm/client.py` and `docs/LLD/llm-provider.md`).

REST vs SDK -- disclosed design
--------------------------------
Google publishes an official `google-generativeai`/`google-genai` Python
SDK, but this implementation calls Gemini's REST endpoint directly via
`httpx`, matching `OllamaClient`/`OpenRouterClient`'s existing pattern
instead of introducing a new third-party SDK dependency into
`agents/pyproject.toml` for a single client. This also keeps the test
strategy identical across all three providers: inject an
`httpx.MockTransport` and assert on the HTTP request/response shape,
exactly as this subtask's own test spec ("HTTP call mocked") calls for.

Endpoint and auth -- disclosed design
----------------------------------------
Gemini's REST convention differs from OpenRouter's in two ways this client
has to account for:

- The model is part of the URL *path* (`/models/{model}:generateContent`),
  not the request body's `"model"` field.
- The API key is passed as a `?key=` query parameter, not an
  `Authorization: Bearer ...` header.

Request/response shape -- disclosed design
----------------------------------------------
Gemini's `generateContent` request body nests the prompt as
`{"contents": [{"parts": [{"text": prompt}]}]}` (Gemini's chat/multi-part
"content" shape) with sampling parameters under a separate
`"generationConfig"` object (`temperature`, `maxOutputTokens`). The
response mirrors this: `{"candidates": [{"content": {"parts": [{"text":
"..."}]}}]}`. `complete()`'s single prompt-in/text-out contract maps onto
this the same way `OpenRouterClient` maps it onto OpenAI's chat shape: one
prompt becomes one single-part user content block, and the first
candidate's first text part is returned.

Default model -- disclosed choice
------------------------------------
`DEFAULT_MODEL = "gemini-2.5-flash"` -- matching the issue body and
`docs/LLD/llm-provider.md`'s "Gemini API (2.5/3.5 Flash) -- alternative for
query-time agents" guidance; 2.5 Flash is used as the concrete default
since it is the current generally-available fast-tier Gemini model.

Error handling -- disclosed design
------------------------------------
Any HTTP-level failure (connection error, timeout, non-2xx status) and any
response-parsing failure (non-JSON body, JSON body missing the expected
`candidates[0].content.parts[0].text` path) raises `GeminiClientError`.
Nothing is silently swallowed or converted into an empty-string/None
result.
"""

from __future__ import annotations

import os

import httpx

from llm.client import LLMClient, LLMError

#: Gemini's Generative Language REST API base URL.
DEFAULT_BASE_URL = "https://generativelanguage.googleapis.com/v1beta"

#: Gemini model name for Gemini 2.5 Flash; see module docstring.
DEFAULT_MODEL = "gemini-2.5-flash"

#: Environment variable used as a fallback when `api_key` is not passed
#: explicitly to the constructor.
API_KEY_ENV_VAR = "GEMINI_API_KEY"

#: Hosted API over the network; same rationale as `OpenRouterClient`'s
#: default -- no local model-loading/inference latency to account for.
DEFAULT_TIMEOUT_SECONDS = 60.0


class GeminiClientError(LLMError):
    """Raised on any Gemini HTTP call failure or malformed response.

    Also raised at construction time if no API key can be resolved (see
    module docstring's "Endpoint and auth" section).
    """


class GeminiClient(LLMClient):
    """`LLMClient` implementation that calls the Gemini API.

    Args:
        api_key: Gemini API key. Defaults to `None`, in which case the
            `GEMINI_API_KEY` environment variable is used. Raises
            `GeminiClientError` immediately if neither is available.
        base_url: Gemini REST API base URL. Defaults to `DEFAULT_BASE_URL`.
        model: Default model name used when a call does not override it
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
            raise GeminiClientError(
                "No Gemini API key available: pass api_key= explicitly "
                f"or set the {API_KEY_ENV_VAR} environment variable."
            )

        self._api_key = resolved_key
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
        """See `LLMClient.complete`. Calls Gemini's `generateContent`."""
        generation_config: dict[str, object] = {"temperature": temperature}
        if max_tokens is not None:
            generation_config["maxOutputTokens"] = max_tokens

        payload: dict[str, object] = {
            "contents": [{"parts": [{"text": prompt}]}],
            "generationConfig": generation_config,
        }

        resolved_model = model or self._model
        request_timeout = timeout if timeout is not None else self._timeout
        endpoint = f"/models/{resolved_model}:generateContent"

        try:
            with httpx.Client(
                base_url=self._base_url, transport=self._transport
            ) as client:
                response = client.post(
                    endpoint,
                    params={"key": self._api_key},
                    json=payload,
                    timeout=request_timeout,
                )
                response.raise_for_status()
        except httpx.HTTPError as exc:
            raise GeminiClientError(
                f"Gemini request to {self._base_url}{endpoint} failed: {exc}"
            ) from exc

        try:
            data = response.json()
        except ValueError as exc:
            raise GeminiClientError(
                f"Gemini response was not valid JSON: {exc}"
            ) from exc

        if not isinstance(data, dict):
            raise GeminiClientError(
                f"Gemini response was not a JSON object: {data!r}"
            )

        candidates = data.get("candidates")
        if not isinstance(candidates, list) or not candidates:
            raise GeminiClientError(
                f"Gemini response missing expected 'candidates' list: {data!r}"
            )

        first_candidate = candidates[0]
        content = (
            first_candidate.get("content")
            if isinstance(first_candidate, dict)
            else None
        )
        parts = content.get("parts") if isinstance(content, dict) else None
        first_part = (
            parts[0] if isinstance(parts, list) and parts else None
        )
        completion = (
            first_part.get("text") if isinstance(first_part, dict) else None
        )

        if not isinstance(completion, str):
            raise GeminiClientError(
                "Gemini response missing expected "
                f"'candidates[0].content.parts[0].text' string: {data!r}"
            )

        return completion
