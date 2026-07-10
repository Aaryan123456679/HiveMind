# Plan — Subtask 4.1.2

1. Create `agents/llm/gemini_client.py`:
   - `DEFAULT_BASE_URL = "https://generativelanguage.googleapis.com/v1beta"`
   - `DEFAULT_MODEL = "gemini-2.5-flash"` (per LLD's "2.5/3.5 Flash" guidance; 2.5 Flash is the
     current stable fast-tier model)
   - `API_KEY_ENV_VAR = "GEMINI_API_KEY"`
   - `DEFAULT_TIMEOUT_SECONDS = 60.0` (hosted API, same rationale as OpenRouter)
   - `GeminiClientError(LLMError)`
   - `GeminiClient(LLMClient)` with `__init__(*, api_key=None, base_url=DEFAULT_BASE_URL,
     model=DEFAULT_MODEL, timeout=DEFAULT_TIMEOUT_SECONDS, transport=None)`, resolving api_key
     from kwarg or env var, raising `GeminiClientError` immediately if neither is available.
   - `complete(...)`: POST to `/models/{model}:generateContent?key={api_key}`, body
     `{"contents": [{"parts": [{"text": prompt}]}], "generationConfig": {"temperature":
     temperature, **({"maxOutputTokens": max_tokens} if max_tokens else {})}}`; parse
     `data["candidates"][0]["content"]["parts"][0]["text"]`; raise `GeminiClientError` on any
     HTTP failure, non-JSON body, or missing/malformed expected keys.
2. Create `agents/llm/test_gemini_client.py` mirroring `test_openrouter_client.py`'s structure:
   interface contract, API-key resolution (explicit kwarg / env fallback / missing-key raises at
   construction), request shape (endpoint path incl. model + query-param key, payload shape,
   model override, base_url/default-model constants), response parsing (happy path), error
   handling (HTTP error status, malformed JSON, missing candidates, empty candidates list, missing
   content/parts/text, connection error), `GeminiClientError` subclasses `LLMError`.
3. Run `cd agents && python3 -m pytest llm/ -q` — full `llm/` suite must pass (existing Ollama +
   OpenRouter tests must remain green, i.e. no accidental interference).
4. Run `cd agents && ruff check llm/gemini_client.py llm/test_gemini_client.py`.
5. Write `validation-matrix.json`, then `self-consistency.json`.
6. One local commit, Problem/Solution/Impact style, no push.
7. Write `handoff.json` with pointers only.
