# Architecture discovery

## Index / LLD read (in mandated order)

1. `docs/LLD/query-agent.md` -- confirms `intent_refiner.py`'s contract at a high level:
   input "raw query + short history", output `{ refined_intent, entities: [], query_type }`.
   No richer `query_type` taxonomy or JSON schema is specified there -- status line says
   "scaffold only". No other LLD doc (`llm-provider.md`, `ingestion-agent.md`, `rpc.md`,
   `graph.md`, `eval.md`) adds anything additional for this subtask; `llm-provider.md`
   governs the `LLMClient` contract (read directly, see below) and `eval.md` only says the
   query pipeline is a benchmarked arm (not relevant to 4.3.1's shape).
2. `gh issue view 22` -- both subtasks read; taxonomy hint taken from 4.3.2 (see
   requirement.md).

## Established pattern (from issue #18/#20, `agents/ingestion/`, `agents/llm/`)

- Agents depend on `llm.client.LLMClient` (abstract, `agents/llm/client.py`) via dependency
  injection -- never a concrete provider (`OllamaClient`/`OpenRouterClient`/`GeminiClient`)
  directly. Confirmed by `llm/client.py`'s own docstring ("no agent module outside
  `agents/llm/` may call a provider SDK/HTTP API directly").
- `LLMClient.complete(prompt, *, model=None, temperature=0.0, max_tokens=None,
  timeout=None) -> str` is the *only* method; callers do their own JSON parsing of the
  returned string.
- Prompt-then-parse-JSON module shape, per `agents/ingestion/segment.py` (3.4.3+3.4.6) and
  `agents/ingestion/propose_split.py` (3.4.5):
  - A private `_build_prompt(...)` function renders a template string.
  - `llm_client.complete(prompt, model=, temperature=, max_tokens=, timeout=)` is called,
    forwarding those four kwargs verbatim from the public function's own signature.
  - Raw completion string is passed through `ingestion._json_fences.strip_code_fences`
    (shared helper, extracted in 3.4.6 to close forwarded finding F1) before `json.loads`.
  - A module-local exception hierarchy: a base `<Module>Error` (NOT a subclass of
    `llm.client.LLMError` -- `LLMError` means "the provider call itself failed"; the
    module's own exception means "the call succeeded but the string wasn't a valid
    <output>") and one concrete `<Module>ParseError` covering every malformed-output case
    with a specific, descriptive message (never a bare `Exception`).
  - Strict-but-minimal field validation: required top-level keys present, correct type per
    field, enum fields checked against a `frozenset` of valid values, list-of-string fields
    checked element-by-element. No deeper semantic validation than the issue's own
    acceptance criteria calls for.
  - Public function returns a `@dataclass(frozen=True)` result type, not a raw dict.
  - `TYPE_CHECKING`-only imports for the injected `LLMClient` type (avoids a hard runtime
    import cycle/dependency at module load time) -- both `segment.py` and (checked)
    `propose_split.py` do this.

## New module note

`agents/query/` currently has only an empty `__init__.py`; `intent_refiner.py` is the
*first* real module here, so there is no existing `agents/query/` sibling code to mirror --
the closest applicable precedent is `agents/ingestion/segment.py` (same
"prompt LLM, strip fences, parse to dataclass, validate loudly" shape as this subtask needs,
just for `agents/query/`'s own field shape instead of segmentation's).

`agents/query/_json_fences.py` is deliberately **not** created as a second copy; the shared
helper already lives at `ingestion._json_fences.strip_code_fences` and both existing
ingestion modules import it from there rather than duplicating it, so `intent_refiner.py`
imports it the same way (`from ingestion._json_fences import strip_code_fences`). Per
`pyproject.toml`, `ingestion`, `query`, `llm`, `eval` are all top-level installed packages
under a common `agents/` root, so `ingestion._json_fences` is an ordinary cross-package
import, not a layering violation -- ingestion doesn't depend on query, only the reverse.

## `LLMClient` contract confirmed (`agents/llm/client.py`)

`complete(prompt, *, model=None, temperature=0.0, max_tokens=None, timeout=None) -> str`;
raises `LLMError` subclass on provider failure. `intent_refiner.refine_intent()` forwards
these same four kwargs, matching `segment()`'s and `propose_split.py`'s own public
signatures.

## Test harness pattern (`ingestion/test_segment.py`)

A small `_FakeLLMClient(LLMClient)` subclass (not `MagicMock(spec=...)`) is the established
"LLMClient mocked" pattern -- returns a canned string or raises a canned error, records calls
for assertion. `test_intent_refiner.py` mirrors this exactly.

## `pyproject.toml` / test config

`[tool.pytest.ini_options] testpaths = ["."]`, run from `agents/` (per dispatch instructions:
`cd agents && python3 -m pytest query/ llm/ -q`). `[tool.setuptools] packages = ["ingestion",
"query", "llm", "eval"]` confirms `query` is already a first-class package even though only
`__init__.py` exists today.
