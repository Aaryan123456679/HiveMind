# Architecture discovery — Subtask 4.1.1

## Token order followed

index/ (none present for agents/llm/) -> memory/+handoffs (none found for
this subtask) -> targeted LLD (`docs/LLD/llm-provider.md`) -> touched
files (`agents/llm/client.py`, `agents/llm/ollama_client.py`,
`agents/llm/test_ollama_client.py`, `agents/llm/__init__.py`,
`agents/pyproject.toml`) -> source (read in full, as above; no other
source files were read before these).

## Relevant docs

- `docs/LLD/llm-provider.md`: defines the provider-agnostic `LLMClient`
  interface requirement; OpenRouter = GPT-4o-mini, used for query-time
  agents; provider selection happens via config (subtask 4.1.3, out of
  scope here); no code outside `agents/llm/` may call a provider SDK
  directly.
- `docs/HLD.md` / `AGENT.md`: corroborate OpenRouter's role (GPT-4o-mini,
  query-time), no additional constraints beyond the LLD.

## Established pattern (from issue #18 / 3.4.1, `ollama_client.py` +
`test_ollama_client.py`)

- Concrete class subclasses `LLMClient` (abc.ABC), implements only
  `complete()`.
- Provider-specific exception subclasses `LLMError`
  (`OllamaClientError(LLMError)`).
- HTTP via `httpx`; constructor accepts a `transport:
  httpx.BaseTransport | None` kwarg solely to let tests inject
  `httpx.MockTransport` — no monkeypatching, no real sockets in tests.
- Constructor kwargs: `base_url`, `model` (default constant
  `DEFAULT_MODEL`), `timeout` (default constant
  `DEFAULT_TIMEOUT_SECONDS`), `transport`.
- `complete()` builds a payload dict, POSTs via a short-lived
  `httpx.Client(base_url=..., transport=...)`, raises the provider error
  on `httpx.HTTPError` (including non-2xx via `raise_for_status()`), on
  non-JSON body, and on a response shape missing/mistyped the expected
  field. Returns the parsed completion text as `str`.
- Test file structure: `_client_with_handler()` helper wrapping
  `httpx.MockTransport`; sections for "LLMClient interface contract",
  "Request shape", "Response parsing", "Error handling" (HTTP error
  status, malformed JSON, missing key, connection error); plus a
  `test_<x>_error_is_llm_error()` check.
- Imports are absolute from the `llm` package root (`from llm.client
  import ...`), consistent with `agents/pyproject.toml`'s
  `[tool.setuptools] packages = ["ingestion", "query", "llm", "eval"]`
  and confirmed working: `cd agents && python3 -m pytest
  llm/test_ollama_client.py -q` -> 13 passed.

## OpenRouter API shape (provider-specific research)

OpenRouter is OpenAI-Chat-Completions-API-compatible:
- Endpoint: `POST https://openrouter.ai/api/v1/chat/completions`
- Auth: `Authorization: Bearer <OPENROUTER_API_KEY>` header (required by
  OpenRouter; unlike local Ollama there is no unauthenticated mode).
- Request body: `{"model": "...", "messages": [{"role": "user",
  "content": "<prompt>"}], "temperature": ..., "max_tokens": ...}`.
  `complete()`'s contract is single-shot prompt-in/text-out with no
  conversation state, so a single `user` message is used (mirrors how
  `OllamaClient` maps `complete()`'s single-prompt contract onto
  `/api/generate`'s single-prompt field — same design rule applied to a
  chat-shaped endpoint).
- Response body (success): `{"choices": [{"message": {"role":
  "assistant", "content": "<completion text>"}, ...}], ...}`. The
  completion text lives at `choices[0].message.content`.
- Default model for this subtask: `openai/gpt-4o-mini` (OpenRouter's
  model-slug convention `<provider>/<model>`), per the issue title
  "OpenRouter (GPT-4o-mini)".
- No official OpenRouter Python SDK is used/required; OpenRouter's API is
  plain HTTP, so `httpx` (already a project dependency, already used by
  `OllamaClient`) is reused rather than adding a new dependency.

## API key handling (new consideration vs. Ollama, disclosed)

Ollama has no auth. OpenRouter requires a bearer API key. Design: accept
an explicit `api_key` constructor kwarg; if not given, fall back to
`os.environ["OPENROUTER_API_KEY"]`; if neither is present, raise
`OpenRouterClientError` at construction time (fail loudly, not on first
`complete()` call, and never silently send an unauthenticated request).
This does not change the `LLMClient.complete()` signature/contract in any
way — it's purely inside the OpenRouter constructor, same seam
`OllamaClient.__init__`'s `base_url`/`model`/`timeout`/`transport` kwargs
use for provider-specific config.

## Impact analysis summary

Purely additive: two new files under `agents/llm/`. No existing file is
modified (no call sites in `agents/ingestion/` or `agents/query/` exist
yet that would need updating — those are out of scope / future subtasks).
`agents/pyproject.toml`'s `packages` list already includes `"llm"`, so no
packaging config change needed.
