# Plan — 3.4.1

1. `agents/llm/client.py`
   - `class LLMError(Exception)`: base exception for provider-agnostic
     failures (kept generic so callers can catch one type regardless of
     provider).
   - `class LLMClient(abc.ABC)` with one abstract method:
     `complete(self, prompt: str, *, model: str | None = None,
     temperature: float = 0.0, max_tokens: int | None = None,
     timeout: float | None = None) -> str`.
   - Docstring discloses ABC-vs-Protocol reasoning and the "single
     `complete()`-shaped method, no speculative methods" rationale.

2. `agents/llm/ollama_client.py`
   - `DEFAULT_MODEL = "llama3.1:8b"`, `DEFAULT_BASE_URL =
     "http://localhost:11434"`, `DEFAULT_TIMEOUT_SECONDS = 120.0`.
   - `class OllamaClientError(LLMError)`.
   - `class OllamaClient(LLMClient)`:
     - `__init__(self, *, base_url=DEFAULT_BASE_URL,
       model=DEFAULT_MODEL, timeout=DEFAULT_TIMEOUT_SECONDS,
       transport: httpx.BaseTransport | None = None)` — `transport` is an
       injectable seam for tests (via `httpx.MockTransport`) without
       requiring a real network call; production code path leaves it
       `None` and `httpx.Client` uses its normal transport.
     - `complete(...)`: POSTs to `{base_url}/api/generate` with JSON body
       `{"model": model or self._model, "prompt": prompt, "stream":
       False, "options": {"temperature": temperature, **(num_predict if
       max_tokens set)}}`. Raises `OllamaClientError` on
       `httpx.HTTPError` (network/timeout/4xx/5xx) and on
       malformed/non-JSON body or a body missing `"response"`. Returns
       `data["response"]`.

3. `agents/llm/test_ollama_client.py`
   - Uses `httpx.MockTransport` injected via `OllamaClient(transport=...)`
     to intercept the HTTP call with zero real network I/O.
   - Assert request shape: method POST, URL path `/api/generate`, JSON
     body contains `model` and `prompt` keys with correct values,
     `stream: False`.
   - Assert response parsing: mock Ollama-shaped JSON response
     (`{"model": ..., "response": "...", "done": true}`) and assert
     `complete()` returns exactly the `"response"` string.
   - Error-path tests: HTTP 500 -> `OllamaClientError`; malformed JSON
     body -> `OllamaClientError`; JSON body missing `"response"` key ->
     `OllamaClientError`.
   - A test asserting `OllamaClient` is-a `LLMClient` (isinstance check)
     and that `LLMClient` cannot be instantiated directly (ABC
     enforcement) — ties the two files' contracts together.

4. Run `agents/.venv/bin/pytest agents/llm/ -v` and
   `agents/.venv/bin/ruff check agents/llm/` before committing
   (self-consistency only).

5. One local commit, Problem/Solution/Impact style matching recent
   `git log` (e.g. `0b541d0`, `770788b`).

6. Write `validation-matrix.json`, `self-consistency.json`,
   `handoff.json`; update `metadata.json` status.
