# Requirement — subtask 4.1.3

Issue #20 ("[4] Query-time LLM providers: OpenRouter + Gemini (agents/llm/)"),
milestone "Phase 4: Query pipeline". This is the **third and final** subtask
of issue #20.

## Subtask 4.1.3 — Config-driven provider selection (no call-site code changes)

Per `gh issue view 20`:

- **Acceptance criteria**: Switching the active provider for a given agent
  role (ingestion vs. query-time) is a config change only; no call sites in
  `agents/ingestion/` or `agents/query/` import a provider SDK directly.
- **Test spec**: `pytest agents/llm/test_provider_selection.py`: assert config
  value X yields client type X for each supported provider; grep-based test
  asserting no provider SDK imports outside `agents/llm/`.
- **Impacted modules**: `agents/llm/factory.py`, `agents/llm/test_provider_selection.py`.

## Confirmed prior state (4.1.1, 4.1.2 — already done, not touched here)

- 4.1.1 (OpenRouter): `agents/llm/openrouter_client.py` + test file. Committed
  `6109d13`, verified PASS_WITH_COMMENTS.
- 4.1.2 (Gemini): `agents/llm/gemini_client.py` + test file. Committed
  `a471268`, verified PASS_WITH_COMMENTS.
- Both already merged locally (not pushed) at HEAD `a471268`.

## Scope for this run

Add exactly one new production module (`agents/llm/factory.py`) plus its test
file (`agents/llm/test_provider_selection.py`). Do not modify
`ollama_client.py`, `openrouter_client.py`, `gemini_client.py`, `client.py`,
or any of the existing three test files — 4.1.1/4.1.2 are already verified
and committed; this run is strictly additive.

## Config mechanism — LLD says "via config, not call-site code changes" but
does not name an env var. Existing three clients already establish a
convention: each resolves its *own* API key via an explicit `api_key=`
constructor kwarg that falls back to a documented env var
(`OPENROUTER_API_KEY`, `GEMINI_API_KEY`), with a module-level
`API_KEY_ENV_VAR` constant naming it. This run extends that same convention
one level up: the factory resolves the *provider name* via an explicit
`provider=` argument that falls back to an env var, `LLM_PROVIDER`, with a
module-level `PROVIDER_ENV_VAR` constant. Supported values: `"ollama"`,
`"openrouter"`, `"gemini"` (module-level constants). Unknown/missing value
raises a clear, typed error (`LLMFactoryError`, subclassing
`agents.llm.client.LLMError` so callers can catch one exception family
across the whole `agents/llm/` package, matching `OllamaClientError`/
`OpenRouterClientError`/`GeminiClientError`'s existing pattern).
