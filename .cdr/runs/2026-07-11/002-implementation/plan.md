# Plan — Subtask 4.1.1

1. Create `agents/llm/openrouter_client.py`:
   - Module docstring disclosing design choices (mirrors
     `ollama_client.py`'s disclosed-design docstring style): endpoint
     choice (chat/completions, single user message), default model
     (`openai/gpt-4o-mini`), API-key handling (explicit kwarg -> env var
     `OPENROUTER_API_KEY` -> raise at construction if absent), error
     handling (never swallow).
   - Constants: `DEFAULT_BASE_URL = "https://openrouter.ai/api/v1"`,
     `DEFAULT_MODEL = "openai/gpt-4o-mini"`, `DEFAULT_TIMEOUT_SECONDS`
     (use a lower default than Ollama's 120s since this is a hosted API,
     not local CPU inference — 60.0).
   - `OpenRouterClientError(LLMError)`.
   - `OpenRouterClient(LLMClient)`:
     - `__init__(self, *, api_key=None, base_url=DEFAULT_BASE_URL,
       model=DEFAULT_MODEL, timeout=DEFAULT_TIMEOUT_SECONDS,
       transport=None)`. Resolve `api_key` from kwarg or
       `os.environ["OPENROUTER_API_KEY"]`; raise
       `OpenRouterClientError` immediately if neither present.
     - `complete(prompt, *, model=None, temperature=0.0, max_tokens=None,
       timeout=None) -> str`: build `{"model":..., "messages": [{"role":
       "user", "content": prompt}], "temperature": temperature}`
       (+ `max_tokens` key only if given, matching Ollama's pattern of
       only including optional keys when not None). POST to
       `/chat/completions` with `Authorization: Bearer <api_key>` header.
       Raise on `httpx.HTTPError`, on non-JSON body, on missing/mistyped
       `choices[0].message.content`.
2. Create `agents/llm/test_openrouter_client.py`, mirroring
   `test_ollama_client.py`'s structure and section headers, using
   `httpx.MockTransport` injected via the `transport` kwarg — no real
   network calls:
   - LLMClient interface contract: `OpenRouterClient` is an instance of
     `LLMClient`.
   - Request shape: POSTs to `/chat/completions`; payload has `model`,
     `messages` (single user-role message = prompt), `temperature`,
     `max_tokens` when given; `Authorization: Bearer <key>` header
     present; default base URL / default model used when not overridden;
     per-call `model` override.
   - API key resolution: explicit `api_key` kwarg used; falls back to
     `OPENROUTER_API_KEY` env var (via `monkeypatch.setenv`); raises
     `OpenRouterClientError` if neither present.
   - Response parsing: extracts `choices[0].message.content` correctly.
   - Error handling: HTTP error status, malformed JSON, missing
     `choices` key, connection error -- all raise
     `OpenRouterClientError`; confirm `OpenRouterClientError` is a
     `LLMError` subclass.
3. Run `cd agents && python3 -m pytest llm/ -q` (full `llm/` suite, both
   old and new tests) and `ruff check llm/openrouter_client.py
   llm/test_openrouter_client.py` (if ruff configured/available).
4. Write `validation-matrix.json` mapping each acceptance-criteria /
   test-spec item to the specific test(s) covering it.
5. Write `self-consistency.json` (build green + matrix coverage; this is
   NOT verification per I4).
6. One local commit, Problem/Solution/Impact style, no push.
7. Write `handoff.json` with pointers only.

No files outside the two new ones are touched. No provider SDK is added
as a dependency (plain `httpx`, already present).
