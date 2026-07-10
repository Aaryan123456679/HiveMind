# Architecture discovery — subtask 4.1.3

## Token order followed
index/ -> memory/+handoffs -> targeted LLD -> touched files -> source.
No pre-existing `.cdr/index/` entry specific to `agents/llm/factory.py`
(module does not exist yet). Prior run handoffs for 4.1.1 (`002-implementation`)
and 4.1.2 (`003-implementation`) read first (see run dirs), then
`docs/LLD/llm-provider.md`, then all of `agents/llm/*.py` + existing tests,
then the two production call sites in `agents/ingestion/` that construct/use
an `LLMClient` (`segment.py`, `propose_split.py`, `wiring.py` — read via
prior compressed context, confirmed no direct provider imports).

## `docs/LLD/llm-provider.md` (read in full)
- States three concrete implementations must exist (Ollama/OpenRouter/Gemini)
  behind a common `LLMClient` interface, and that "Selection between
  providers happens via config, not call-site code changes."
- Does **not** name a specific env var or config-file field for provider
  selection — this run must pick a convention (per dispatch instructions) and
  document it (done in `requirement.md` above).
- "Design rule": no code outside `agents/llm/` may call a provider SDK
  directly; `agents/ingestion/` and `agents/query/` only depend on the
  `LLMClient` interface.

## `agents/llm/client.py`
- `LLMClient` is an `abc.ABC` with one abstract method,
  `complete(prompt, *, model=None, temperature=0.0, max_tokens=None,
  timeout=None) -> str`.
- `LLMError` is the base exception; each concrete client subclasses it
  (`OllamaClientError`, `OpenRouterClientError`, `GeminiClientError`).

## `agents/llm/ollama_client.py`
- `OllamaClient(*, base_url=DEFAULT_BASE_URL, model=DEFAULT_MODEL,
  timeout=DEFAULT_TIMEOUT_SECONDS, transport=None)`. No API key (local
  server) — constructible with zero required args.

## `agents/llm/openrouter_client.py`
- `OpenRouterClient(*, api_key=None, base_url=DEFAULT_BASE_URL,
  model=DEFAULT_MODEL, timeout=DEFAULT_TIMEOUT_SECONDS, transport=None)`.
- API key resolution pattern (the convention this run extends):
  explicit kwarg first, else `os.environ.get(API_KEY_ENV_VAR)` where
  `API_KEY_ENV_VAR = "OPENROUTER_API_KEY"` is a module constant; raises
  `OpenRouterClientError` immediately at construction if neither resolves.

## `agents/llm/gemini_client.py`
- `GeminiClient(*, api_key=None, base_url=DEFAULT_BASE_URL,
  model=DEFAULT_MODEL, timeout=DEFAULT_TIMEOUT_SECONDS, transport=None)`.
- Identical API-key resolution convention, `API_KEY_ENV_VAR =
  "GEMINI_API_KEY"`.

## Existing tests (`test_ollama_client.py`, `test_openrouter_client.py`,
`test_gemini_client.py`)
- All inject `httpx.MockTransport` via the `transport=` kwarg; no real
  network calls. `pytest.ini`/`pyproject.toml` runs with `agents/` as the
  import root (`from llm.client import ...`, `from llm.ollama_client import
  ...` — no `agents.` prefix).
- Confirmed via `cd agents && python3 -m pytest llm/ -q` conventions in prior
  runs' handoffs (52 passed before this run).

## Call sites in `agents/ingestion/` (read via prior session's compressed
context + targeted grep)
- `agents/ingestion/segment.py` and `agents/ingestion/propose_split.py`
  depend only on `LLMClient` (type-only, no concrete provider import).
- `agents/ingestion/wiring.py` is unrelated (grpc `SegmentWiringClient`, no
  LLM dependency at all).
- Test-only files `agents/ingestion/test_segment_live.py` and
  `agents/ingestion/test_e2e_smoke.py` import `OllamaClient` **directly** —
  this is pre-existing, deliberate (live/e2e smoke tests that need a
  concrete, real-network-capable client, not the plain interface) and is
  explicitly out of scope for this run (do not touch; the issue's
  acceptance criteria says "no call sites... import a provider SDK
  directly" — these two files are test harnesses, not agent call sites, and
  predate this subtask). This is flagged forward in the grep-based test's
  design below so it does not spuriously fail on legitimate pre-existing
  test fixtures.
- `agents/query/` is empty (`__init__.py` only) — no call sites exist there
  yet, so the grep test can only assert an invariant over the current
  (empty) state; it will start actually testing something once query-time
  call sites are added in a future subtask.

## Design decision — grep-based test scope
The acceptance criteria's "provider SDK" phrase, read literally, would mean
third-party SDKs like `google-generativeai`/`openai` — none of which any of
the three existing clients use (all three use `httpx` directly, per each
module's own "disclosed design" docstring). Read that way the grep test
would be vacuous today. The test spec's intent (config-driven selection,
call sites decoupled from concrete providers) is better served by asserting
the concrete invariant the LLD's "Design rule" section actually states: no
*production* (non-test) module under `agents/ingestion/` or `agents/query/`
imports `llm.ollama_client`, `llm.openrouter_client`, or `llm.gemini_client`
(the concrete client modules) or a third-party provider SDK module name
(`google.generativeai`, `openai`, `anthropic`). Test files are excluded from
this scan since `test_segment_live.py`/`test_e2e_smoke.py`'s direct
`OllamaClient` use is pre-existing and intentional (disclosed above), not
something this subtask's acceptance criteria was written to flag.
