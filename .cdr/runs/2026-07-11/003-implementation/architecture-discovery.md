# Architecture Discovery — Subtask 4.1.2

## Relevant docs (read before source, per token order)
- `docs/LLD/llm-provider.md` — provider-agnostic `LLMClient` interface; Ollama (ingestion),
  OpenRouter (query-time), Gemini 2.5/3.5 Flash (query-time alternative). Selection via config
  only (4.1.3, out of scope here).

## Reference implementations read
- `agents/llm/client.py` — `LLMClient(abc.ABC)` with single abstract method
  `complete(prompt, *, model=None, temperature=0.0, max_tokens=None, timeout=None) -> str`.
  Raises `LLMError` subclasses on any failure; never returns empty/partial silently.
- `agents/llm/ollama_client.py` — `OllamaClient`: httpx POST to local `/api/generate`,
  `httpx.BaseTransport` injectable via `transport=` kwarg for test mocking (no monkeypatching,
  no real sockets), `DEFAULT_BASE_URL`/`DEFAULT_MODEL`/`DEFAULT_TIMEOUT_SECONDS` module constants,
  a dedicated `<Provider>ClientError(LLMError)` exception, explicit checks at every parse step.
- `agents/llm/openrouter_client.py` (4.1.1, concurrent/reference pattern) — `OpenRouterClient`:
  same shape, hosted API requiring bearer-token auth resolved from `api_key=` kwarg or
  `OPENROUTER_API_KEY` env var, failing fast at construction if neither is available.
- `agents/llm/test_ollama_client.py` / `agents/llm/test_openrouter_client.py` — test structure:
  `_client_with_handler(handler, **kwargs)` helper wrapping `httpx.MockTransport`; sections for
  interface contract, (API-key resolution where applicable), request shape, response parsing,
  error handling (HTTP error status, malformed JSON, missing expected key(s), connection error,
  exception subclassing).

## Gemini-specific findings
- Gemini's REST API (Generative Language API) exposes
  `POST https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent`
  with the API key passed as a `?key=` query parameter (not a bearer header) — this is Gemini's
  documented REST auth convention, distinct from OpenRouter's `Authorization: Bearer` header.
  Request body shape: `{"contents": [{"parts": [{"text": prompt}]}],
  "generationConfig": {"temperature": ..., "maxOutputTokens": ...}}`.
  Response shape: `{"candidates": [{"content": {"parts": [{"text": "..."}]}}]}`.
- No `google-generativeai` (or `google-genai`) SDK dependency exists in
  `agents/pyproject.toml` (only `httpx`, `fastapi`, `pydantic`, `grpcio`, `pymupdf`). Adding a new
  SDK dependency for this one client would break the established httpx-REST pattern used by both
  existing providers and would require a `pyproject.toml` dependency-list change (out of scope for
  a single-file-pair subtask). The dispatch instructions explicitly override the issue's "SDK call
  mocked" wording with "HTTP call mocked" — confirming the REST/httpx approach is the intended one
  here, consistent with `OllamaClient`/`OpenRouterClient`.
- Decision: implement `GeminiClient` as a third httpx-based `LLMClient`, following the exact same
  constructor/error/test conventions as `OpenRouterClient`, swapping only: endpoint path template
  (`/v1beta/models/{model}:generateContent`), auth mechanism (`?key=` query param instead of
  bearer header), request/response JSON shape (Gemini's `contents`/`candidates` shape instead of
  OpenAI's `messages`/`choices` shape), and default model/env-var names.

## Files that will change
- New: `agents/llm/gemini_client.py`
- New: `agents/llm/test_gemini_client.py`
- No existing files modified (additive only, per dispatch's "purely additive new-file work" note).
