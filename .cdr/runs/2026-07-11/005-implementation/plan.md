# Plan — subtask 4.1.3

## 1. `agents/llm/factory.py`

- Module constants:
  - `PROVIDER_ENV_VAR = "LLM_PROVIDER"` — env var fallback when `provider=`
    not passed explicitly (documented convention choice, see requirement.md).
  - `PROVIDER_OLLAMA = "ollama"`, `PROVIDER_OPENROUTER = "openrouter"`,
    `PROVIDER_GEMINI = "gemini"` — supported provider name constants.
  - `SUPPORTED_PROVIDERS = (PROVIDER_OLLAMA, PROVIDER_OPENROUTER, PROVIDER_GEMINI)`.
- `class LLMFactoryError(LLMError)` — raised for unknown/missing provider
  value, subclassing the shared `agents.llm.client.LLMError` family.
- `def create_llm_client(provider: str | None = None, **client_kwargs) -> LLMClient`:
  1. Resolve `provider`: explicit arg first, else
     `os.environ.get(PROVIDER_ENV_VAR)`.
  2. If still `None`/empty -> raise `LLMFactoryError` naming the env var.
  3. Case-insensitive match against `SUPPORTED_PROVIDERS`
     (normalize `.strip().lower()`) so e.g. `"Ollama"`/`" ollama "` both work
     — small robustness improvement, not required by spec but cheap and
     consistent with "config value" being possibly human-typed.
  4. Dispatch via a `dict[str, type[LLMClient]]` mapping to the three
     concrete classes, passing through `**client_kwargs` unchanged so
     callers can still supply `api_key=`, `model=`, `transport=`, etc.
  5. Unknown value -> raise `LLMFactoryError` listing `SUPPORTED_PROVIDERS`.
- No provider SDK import added; the module only imports the three existing
  concrete client classes (which is `agents/llm/`'s own internal concern,
  not a "call site" per the design rule — the design rule is about code
  *outside* `agents/llm/`).

## 2. `agents/llm/test_provider_selection.py`

- One test per supported provider asserting
  `type(create_llm_client("ollama")) is OllamaClient` (and same for
  `openrouter`/`gemini`), including a case-insensitive variant.
- Test that env-var fallback works (`monkeypatch.setenv("LLM_PROVIDER",
  ...)`, call `create_llm_client()` with no arg).
- Test that explicit `provider=` arg takes precedence over the env var.
- Test unknown provider string -> `LLMFactoryError` raised, with the
  offending value's message content asserted.
- Test missing provider (no arg, no env var, `monkeypatch.delenv` to be
  sure) -> `LLMFactoryError` raised.
- Test that `**client_kwargs` are forwarded (e.g. pass `transport=` to the
  ollama case, `api_key=` to openrouter/gemini cases, assert the resulting
  instance's private attribute reflects it — mirrors the constructor kwarg
  contract each concrete client already exposes).
- Grep-based test: walk `agents/ingestion/` and `agents/query/`, excluding
  files named `test_*.py`, assert none import
  `llm.ollama_client`/`llm.openrouter_client`/`llm.gemini_client` or a
  hard-coded list of known third-party provider SDK module names
  (`google.generativeai`, `openai`, `anthropic`). Implemented with a simple
  substring/AST-free regex scan (consistent with "grep-based" wording in
  the test spec), not a full import-graph analysis.

## 3. Validation
- `cd agents && python3 -m pytest llm/ -q` — must show 52 previously-passing
  + N newly-added, 0 failures.
- `cd agents && python3 -m ruff check llm/factory.py llm/test_provider_selection.py`.

## 4. Commit
- Single local commit, Problem/Solution/Impact style, no push.

## 5. Handoff
- `handoff.json` with pointers only; explicit `self_verification_performed:
  false` per invariant I4; note this is issue #20's final subtask, awaiting
  independent `/cdr:verify`.
