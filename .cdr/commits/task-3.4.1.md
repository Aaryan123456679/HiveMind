# task-3.4.1 — LLMClient ABC + Ollama implementation (agents/llm/)

**Issue:** #18 ("[3] Segmentation agent", milestone #5 "Phase 3")
**Subtask:** 3.4.1 (1 of 6)
**State:** verified
**Verdict:** PASS_WITH_COMMENTS

## Summary

First of six subtasks under GitHub issue #18 ("segmentation agent",
milestone #5, "Phase 3"). Adds the provider-agnostic `LLMClient` interface
(`agents/llm/client.py`) and a working local-Ollama-backed implementation
(`agents/llm/ollama_client.py`), so that later subtasks in this issue —
the segmentation agent itself (3.4.3) and `ProposeSplit` (3.4.5) — have a
common, tested abstraction to route LLM calls through rather than calling
a provider SDK/HTTP API directly. `agents/llm/` was previously
scaffold-only (an empty `__init__.py`); this is the first real code in
that package. Purely additive: no existing module was modified.
Independently re-verified **PASS_WITH_COMMENTS**, no fix cycle needed.

**Issue #18 has 5 subtasks remaining (3.4.2–3.4.6) and is NOT ready to
close.**

## Features

- `LLMClient` (`client.py`): an `abc.ABC` with a single abstract method,
  `complete(prompt, *, model=None, temperature=0.0, max_tokens=None,
  timeout=None) -> str`. `ABC` was deliberately chosen over
  `typing.Protocol` for runtime enforcement (a concrete subclass that
  forgets to implement `complete()` fails immediately at instantiation),
  matching this codebase's existing preference for concrete/instantiable
  types over structural typing. The single-method shape is deliberately
  generic enough to serve both known future call sites (structured-JSON
  segmentation output and text-splitting) without conversation state,
  streaming, or tool-calling support, none of which either call site
  currently needs.
- `LLMError`: a provider-agnostic base exception so callers elsewhere in
  `agents/` can catch one exception type regardless of which provider is
  configured.
- `OllamaClient` (`ollama_client.py`): the first concrete `LLMClient`,
  calling a local Ollama server's `/api/generate` endpoint (chosen over
  `/api/chat` since `complete()`'s contract is single-shot, prompt-in/
  text-out, with no conversation state to carry). Configurable
  `base_url` (default `http://localhost:11434`), `model` (default
  `"llama3.1:8b"`, per the issue body and `docs/LLD/llm-provider.md`'s
  cost-at-volume guidance), and `timeout` (default 120s, generous for
  local CPU 8B-class inference). Accepts an optional `transport` override
  for test injection.
- `OllamaClientError` (subclasses `LLMError`): raised on any HTTP-level
  failure (connection error, timeout, non-2xx status) or response-parsing
  failure (non-JSON body, missing `"response"` key, or a `"response"`
  value that isn't a string). Nothing is silently swallowed into an
  empty-string/`None` result.
- `agents/llm/test_ollama_client.py`: covers the interface contract,
  request shape (endpoint/payload), response parsing, and error paths
  (HTTP failure, connection error, malformed JSON, missing key) entirely
  via `httpx.MockTransport` — no real network calls anywhere in the suite.
- No new dependency: `httpx` was already declared in `agents/pyproject.toml`
  prior to this change.

## Impact

- `agents/ingestion/segment.py` (3.4.3) and `agents/ingestion/propose_split.py`
  (3.4.5) — neither built yet — now have a real, tested provider
  abstraction to depend on instead of calling Ollama (or any future
  provider) directly, enforcing the architectural rule that only
  `agents/llm/` may touch a provider SDK/HTTP API.
- No existing call sites changed: `rg -i ollama` across `agents/`
  (excluding the new `agents/llm/` files) returned no matches both before
  and after this change — `agents/llm/` was genuinely unused scaffold
  until now.
- One new non-blocking finding, disclosed and recorded (not a defect,
  a test-coverage gap): `OllamaClient.complete()` correctly raises
  `OllamaClientError` when the response JSON's `"response"` key is
  present but holds a non-string value (e.g. an int) — the `isinstance`
  guard at `ollama_client.py:128-131` is implemented correctly and was
  independently hand-verified during verification — but
  `test_ollama_client.py` has no dedicated test exercising that specific
  path. Recorded in `.cdr/index/regression.jsonl` and
  `.cdr/memory/pending.md`; forward-referenced to milestone #10 per
  standing convention, no dedicated GitHub issue created directly now.
- A separate housekeeping commit (`987b5f6`) finalized the implementation
  run's `handoff.json`/`metadata.json` after the original implementation
  session was cut off by a session-limit error; independently confirmed
  via `git show` that it touches only `.cdr/runs/2026-07-09/030-implementation/`
  run artifacts, no source files — no functional change, no impact on the
  verification verdict below.

## Verification

- **Verdict:** PASS_WITH_COMMENTS (no fix cycle required)
- **Run ID:** `.cdr/runs/2026-07-10/001-verification`
- **Commit reviewed:** `e7d1e07fd3a8716b47fa4be12abae2038e40021b`
  (follow-up `987b5f6eafe0745f577d4f8c5a7d11f89f74c9ea` also checked, scope
  confirmed run-artifacts-only)
- All 9 verification dimensions independently checked: `requirements`
  (PASS), `architecture_conformance` (PASS — matches
  `docs/LLD/llm-provider.md`), `regression_risk` (PASS — additive only;
  full `agents/` suite run 3x, 67/67 passing each time, no flakiness),
  `edge_cases_and_error_handling` (PASS_WITH_COMMENT — 5 error scenarios
  independently hand-verified against the implementation via a throwaway
  script; all 5 correctly raise `OllamaClientError`; the non-string-
  `response`-value path is correct but untested, per the finding above),
  `security` (PASS — no secrets, no injection vector in HTTP call
  construction), `performance` (PASS — reasonable timeout default; noted,
  non-blocking, that `httpx.Client` is opened/closed per call rather than
  reused, fine at current call volume), `maintainability` (PASS —
  extensive disclosed-design docstrings for future 3.4.3/3.4.5
  maintainers), `test_coverage` (PASS_WITH_COMMENT — 13/13 targeted tests
  independently confirmed passing, all via `httpx.MockTransport`, no real
  sockets), `confidence` (high — Ollama `/api/generate` request/response
  shape independently re-derived from the verifier's own knowledge,
  confirmed to match; `pyproject.toml` diff confirmed empty, so "no new
  dependency" claim holds).
- Zero must-fix findings. The one non-blocking finding is the missing
  non-string-`response`-value test described above.
- A forward-looking, explicitly non-blocking observation was also raised:
  `complete()`'s current signature (prompt, model, temperature, max_tokens,
  timeout) has no separate system-prompt or chat-message-list parameter;
  fine if 3.4.3/3.4.5 truly only need single-shot prompt-in/text-out as
  currently documented, but would need a low-risk additive signature
  change if segmentation prompting later wants a dedicated system-prompt
  field. Not acted on now — no current call site requires it.
- Verification also independently confirmed a prompt-injection attempt:
  this session's tool-output channel (and the prior verification run's)
  again carried embedded fake system-reminder-style text — a fake
  date-change notice instructing silence, fake "tokensave" MCP-server
  usage instructions for a tool never actually available in this session,
  and a fake "Auto Mode Active" directive. Consistent with this repo's
  known, recurring injection pattern (also seen and disclosed during
  3.3.3/3.3.4). Treated as untrusted data only; none of its instructions
  were followed; disclosed here per protocol.

## Release Notes

- Added `agents/llm/client.py` (`LLMClient` ABC, `LLMError`) and
  `agents/llm/ollama_client.py` (`OllamaClient`, `OllamaClientError`): a
  provider-agnostic LLM completion interface plus a working local-Ollama
  implementation (`/api/generate`, default model `llama3.1:8b`), the first
  real code in the previously-scaffold-only `agents/llm/` package.
- No existing code changed; purely additive.
- Known non-blocking follow-up (not fixed now): no dedicated test for
  `OllamaClient.complete()`'s non-string-`"response"`-value error path
  (implementation independently confirmed correct). Forward-referenced to
  GitHub milestone #10.
- Commits: `e7d1e07fd3a8716b47fa4be12abae2038e40021b` (implementation),
  `987b5f6eafe0745f577d4f8c5a7d11f89f74c9ea` (run-metadata finalization
  only, no functional change). Both local-only, not pushed.
- **Issue #18 status:** subtask 1 of 6 complete and verified. Subtasks
  3.4.2 through 3.4.6 remain outstanding. This record does not close, or
  otherwise change the state of, issue #18 on GitHub.
