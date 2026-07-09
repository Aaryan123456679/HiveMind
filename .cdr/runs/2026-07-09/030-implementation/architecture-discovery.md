# Architecture discovery — 3.4.1

## Existing state confirmed

- `agents/llm/` exists but contains only an empty `agents/llm/__init__.py`
  (per `docs/LLD/llm-provider.md`: "Status: scaffold only"). No prior
  client/provider code to preserve or migrate.
- `agents/pyproject.toml` already declares `httpx>=0.27` as a runtime
  dependency and lists `"llm"` in `[tool.setuptools] packages`. No new
  dependency needs to be added — `httpx` is used for the Ollama HTTP call
  (it also has a well-supported mock transport, `httpx.MockTransport`, used
  by the test file per the issue's own guidance).
- Sibling subpackage `agents/ingestion/` establishes conventions followed
  here:
  - `from __future__ import annotations` at top of every module.
  - snake_case identifiers; frozen dataclasses for simple value shapes.
  - Module + function docstrings citing the issue/subtask number and
    disclosing any deliberate deviations from the issue's literal wording
    (e.g. `rawdoc.py`'s snake_case-vs-camelCase disclosure).
  - Tests use `pytest`, live alongside the module as `test_<module>.py`,
    use `monkeypatch`/mocking rather than real I/O.
  - `agents/.venv` is the venv used to run tests (`agents/.venv/bin/pytest`).
- `docs/LLD/llm-provider.md` (existing scaffold LLD) specifies:
  - A common `LLMClient` protocol/ABC with concrete implementations per
    provider (Ollama for ingestion-time; OpenRouter/Gemini for query-time —
    out of scope here, future subtasks/issues).
  - "No code outside `agents/llm/` may call provider SDK directly" — this
    subtask's Ollama implementation is the only place allowed to speak HTTP
    to Ollama.
  - Ollama default model reasoning: "Llama 3.1 8B, chosen for cost at high
    call volume" (ingestion-time segmentation calls Ollama per-document).
- `docs/LLD/ingestion-agent.md` confirms `agents/llm/` is "provider
  abstraction for the Ollama call" used by the not-yet-built segmentation
  agent — consistent with the issue body's downstream-consumer note.

## Design decisions (disclosed)

1. **ABC, not `typing.Protocol`.** Chosen because: (a) the sibling
   `agents/ingestion/` codebase already favors concrete, instantiable base
   classes/dataclasses over structural typing; (b) an ABC lets us provide a
   single documented contract with `abstractmethod` enforcement at
   instantiation time (fails fast if `OllamaClient` — or a future
   OpenRouter/Gemini client — forgets to implement `complete()`), which is
   preferable in a small provider-swap surface where "no other module calls
   the provider SDK directly" is a hard architectural rule worth enforcing
   structurally, not just via type-checker hints. `typing.Protocol` would
   work too (duck typing, no inheritance required) but offers no runtime
   enforcement, and this codebase has not used `Protocol` elsewhere.
2. **`complete(prompt: str, *, model: str | None = None, temperature: float
   = 0.0, max_tokens: int | None = None, timeout: float | None = None) ->
   str`** as the single abstract method. This satisfies both future call
   sites per the issue's own downstream-awareness note:
   - `segment.py` (3.4.3): structured JSON output — needs a plain prompt
     string in, plain text string out (caller does its own JSON parsing of
     the returned string); low temperature for determinism.
   - `propose_split.py` (3.4.5): text-splitting — same shape, just a
     different prompt.
   No chat-message-list, no streaming, no tool-calling — those are
   speculative and not required by either known downstream consumer, so
   they are deliberately omitted (issue explicitly asks not to
   over-design).
3. **Ollama endpoint: `/api/generate`, not `/api/chat`.** Both call sites
   are single-shot "prompt in, text out" (no multi-turn conversation state,
   no system/user/assistant role structure needed for segmentation/split
   prompts), so the simpler `/api/generate` endpoint's request/response
   shape (`{"model", "prompt", "stream": false}` -> `{"response": "..."}`)
   maps directly onto `complete()` without translation overhead. `/api/chat`
   would require constructing a messages list for no benefit here.
4. **Default model string: `"llama3.1:8b"`** — the exact Ollama model tag
   for Llama 3.1 8B (matches Ollama's model library naming convention,
   e.g. `ollama pull llama3.1:8b`), per the issue's "default e.g. Llama 3.1
   8B" and the LLD's same wording.
5. **Configurable base URL** (default `http://localhost:11434`, Ollama's
   standard local default), **configurable model**, and a **timeout**
   (default 120s — local 8B-class generation can be slow on CPU; explicit
   `timeout` param on `complete()` overrides).
6. **Error handling**: `httpx.HTTPStatusError`/`httpx.RequestError` from a
   non-2xx response or connection failure propagate as a raised
   `OllamaClientError` (new small exception type) wrapping the original;
   malformed/non-JSON response body or a JSON body missing the expected
   `"response"` key also raises `OllamaClientError` with a clear message.
   Nothing is silently swallowed, per the issue's explicit requirement.

## No other agent module currently calls Ollama

`rg -i ollama` across `agents/` (outside the new `agents/llm/` files)
returns nothing — confirms the "no other module calls Ollama directly"
acceptance criterion is trivially satisfied pre-existing, and this subtask
does not need to migrate any call sites.
