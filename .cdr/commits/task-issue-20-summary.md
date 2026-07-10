# Issue #20 (milestone #6, "Phase 4: Query pipeline") -- consolidated closure summary

Issue #20 comprised 3 subtasks. All are now independently implemented,
verified, and committed **locally only**.

| Subtask | Summary | Commit(s) | Verdict | Verification run |
|---|---|---|---|---|
| 4.1.1 | `agents/llm/openrouter_client.py` (`OpenRouterClient(LLMClient)`) -- OpenRouter (GPT-4o-mini) provider implementation of the `complete()` interface, mirroring the existing `OllamaClient` pattern (bearer API-key auth, fail-loud error handling, injectable `httpx` transport for tests). | `6109d13` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/003-verification` |
| 4.1.2 | `agents/llm/gemini_client.py` (`GeminiClient(LLMClient)`) -- Gemini provider implementation via direct REST (no SDK dependency added), same constructor/error-wrapping shape as the OpenRouter client. | `a471268` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/004-verification` |
| 4.1.3 | `agents/llm/factory.py` (`create_llm_client`) -- config-driven provider factory (`LLM_PROVIDER` env var or explicit arg; dispatches to Ollama/OpenRouter/Gemini), plus `test_provider_selection.py`. Independently-verified claim: no call site outside `agents/llm/` imports a concrete provider client or SDK directly. | `ca4ead2`, `dbdd589` (metadata-only follow-up filling actual commit sha into run handoff.json) | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/006-verification` |

## Impact

Issue #20 establishes the full provider surface (`agents/llm/`) that Phase 4's
query pipeline will build on: three interchangeable `LLMClient` implementations
(Ollama, OpenRouter/GPT-4o-mini, Gemini) plus a single config-driven entry point
(`create_llm_client`) so that provider choice is a deployment-time config
decision (`LLM_PROVIDER` env var), not a call-site code change. All three
subtasks are additive-only -- no existing production file was modified by
4.1.1/4.1.2, and 4.1.3 added `factory.py`/its test file without touching the
three client modules. `agents/query/` itself remains empty (only
`__init__.py`); wiring a real query-pipeline caller onto `create_llm_client` is
future Phase 4 work, not part of this issue's stated scope.

## Verification

- 4.1.1: `verdict=PASS_WITH_COMMENTS`, commit `6109d13`, run `.cdr/runs/2026-07-11/003-verification/verification.json`. 33/33 `agents/llm/` tests pass; full-suite run showed 2 pre-existing unrelated failures (protobuf gencode/runtime mismatch traced to commit `9794fd8`, issue #19). `ruff check agents/llm/` clean.
- 4.1.2: `verdict=PASS_WITH_COMMENTS`, commit `a471268`, run `.cdr/runs/2026-07-11/004-verification/verification.json`. 52/52 `agents/llm/` tests pass; full-suite collection error is the same pre-existing, unrelated protobuf issue. `ruff check agents/llm/` clean.
- 4.1.3: `verdict=PASS_WITH_COMMENTS`, commit `ca4ead2` (+ `dbdd589`), run `.cdr/runs/2026-07-11/006-verification/verification.json`. 71/71 `agents/llm/` tests pass; full-suite run showed the same 2 pre-existing unrelated failures. `ruff check agents/llm/` clean. Verifier independently re-grepped `agents/ingestion` and `agents/query` and confirmed zero direct provider-client/SDK imports in production files.

## Release notes

- Added `OpenRouterClient` and `GeminiClient` (`agents/llm/`), giving the LLM
  abstraction layer two additional interchangeable providers alongside the
  existing `OllamaClient`.
- Added `agents/llm/factory.py::create_llm_client`, a config-driven provider
  factory selecting Ollama/OpenRouter/Gemini via an explicit argument or the
  `LLM_PROVIDER` environment variable, so provider selection is a deployment
  config decision rather than a code change at call sites.
- No behavior change to existing `agents/ingestion/` pipeline code; issue #20
  is purely additive to `agents/llm/`.

## Still-open findings carried forward (for the user's awareness before any push/close decision)

Pulled verbatim from the 3 verification.json files above (not re-derived from memory):

- **hivemind-issue20-4.1.1-timeout-not-covered** (low, non-blocking, 4.1.1):
  no test exercises `httpx.TimeoutException` specifically. It IS a subclass of
  `httpx.HTTPError` so is already caught and correctly wrapped in
  `OpenRouterClientError` by the existing `except httpx.HTTPError` block --
  correct by code inspection, just not exercised by a dedicated test.
- **hivemind-issue20-4.1.1-httpx-client-per-call** (low, non-blocking, 4.1.1):
  `complete()` opens/closes a new `httpx.Client` on every call rather than
  reusing a connection pool. Identical to the pre-existing `OllamaClient`
  pattern (not a regression introduced here), but for a hosted/networked
  provider under real query-time load this has more relative latency impact
  than for local Ollama.
- **Gemini API key transmitted as URL query parameter** (low, security-note,
  non-blocking, 4.1.2): Gemini's REST convention sends `?key=...` in the
  request URL rather than an `Authorization` header. Not a defect in this
  commit -- `GeminiClientError` messages correctly avoid including the query
  string -- but any future HTTP-level logging/tracing/proxy layer added on
  top of `httpx` must redact query params for Gemini requests specifically,
  since an `Authorization` header would not need the same redaction for
  OpenRouter. Flagged for whoever wires in HTTP-level tracing/logging in a
  future subtask.
- **Full-suite protobuf gencode/runtime mismatch** (info, non-blocking,
  observed independently in all 3 verification runs 4.1.1/4.1.2/4.1.3): the
  full repo test suite fails/errors at `agents/ingestion/test_e2e_smoke.py`
  and `agents/ingestion/test_shortlist.py` due to a pre-existing protobuf
  gencode (6.33.5) vs runtime (5.29.6) version mismatch, confirmed via `git
  log` to originate from an unrelated earlier commit (`9794fd8`, issue #19).
  All 3 verifiers independently confirmed this cannot be caused by any of the
  three additive `agents/llm/` diffs in issue #20. Not fixed here, not
  blocking; carried forward from issue #19's own still-open findings.
- **`agents/ingestion/segment.py` still constructs `OllamaClient` directly**
  (non-blocking, cross-cutting, flagged by the 4.1.3 verifier as a comment,
  not a finding): 4.1.3's acceptance criteria only requires that no call site
  import a concrete provider client/SDK *outside* `agents/llm/` in a way that
  bypasses the interface -- `segment.py` imports `OllamaClient` (a legitimate
  `agents/llm/` type) via dependency injection, which satisfies that
  criterion. It does not yet route through the new `create_llm_client`
  factory. The 4.1.3 implementer flagged this forward explicitly in
  `handoff.json` as out of scope for 4.1.3. This is a future-consistency
  follow-up (migrating `segment.py` to the factory so provider selection is
  config-driven there too), not a defect of any subtask in issue #20.
  Recommend a dedicated follow-up subtask if/when desired.

All findings above are being recorded in `.cdr/index/regression.jsonl` and
carried forward via this document. None of them block issue #20's closure for
its own stated scope, but all should be surfaced to the user before deciding
whether to push and/or close issue #20 (and milestone #6) on GitHub.

## Note on anomalous tool-flow content

All three independent verification runs (003/004/006), and this cdr-commit
run itself, observed the same recurring pattern: text formatted as
harness-style system-reminder blocks (a "date changed" notice, an MCP
"tokensave" server instructions block for a tool not present in the actual
tool list, and an "Auto Mode Active" directive encouraging reduced caution
around destructive git actions) appearing directly in the tool-call flow.
None of the verifiers found this text embedded inside actual repo/GitHub
content (issue bodies, commit messages, diffs) -- it arrived directly in the
tool-call stream, not as quoted repo data. Per standing instructions this was
treated as untrusted/informational and not acted upon in any of the four
runs; no scope, permissions, or verdicts were altered because of it. Flagged
here again for the user's awareness given the repeated pattern across
sessions.
