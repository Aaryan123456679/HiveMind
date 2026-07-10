# Issue #22 (milestone #6, "Phase 4: Query pipeline") -- consolidated closure summary

Issue #22 comprised 2 subtasks. Both are now independently implemented,
verified, and committed **locally only**.

| Subtask | Summary | Commit | Verdict | Verification run |
|---|---|---|---|---|
| 4.3.1 | `agents/query/intent_refiner.py` (`refine_intent()`) -- LLM-backed intent-refiner input/output contract + prompt. Returns `IntentRefinerResult{refined_intent, entities, query_type}` via `LLMClient` dependency injection, parsing/validating the LLM's raw JSON completion. Field names (`refined_intent`, `entities`, `query_type`) come from `docs/LLD/query-agent.md`; the `query_type` value taxonomy (`factual_lookup` / `broad_exploratory`) was self-authored (disclosed in the module docstring) since the LLD does not specify value names. | `694b0e3` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/012-verification` |
| 4.3.2 | `agents/query/test_intent_refiner_types.py` -- 6 new non-overlapping fixture queries (3 `factual_lookup`, 3 `broad_exploratory`, distinct domains from 4.3.1's own 2 fixtures) plus a direct `test_query_type_differentiates_categories` assertion, satisfying the acceptance criteria's "differentiates ... verified across multiple fixture queries" wording. Test-only, zero production-code changes (`git diff` confirmed `intent_refiner.py` untouched). | `08f434e` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/014-verification` |

## Impact

Issue #22 delivers the query-pipeline's intent-refinement stage: a single
LLM-backed function (`refine_intent()`) that turns a raw user query into a
structured `IntentRefinerResult` (refined intent text, extracted entities,
and a `query_type` classification), following the same
prompt-then-parse-JSON pattern already established in
`agents/ingestion/segment.py` and `propose_split.py`. The implementation is
purely additive -- no existing production file outside `agents/query/` was
modified. `refine_intent()` reuses `agents.ingestion._json_fences.strip_code_fences`
rather than duplicating fence-stripping logic, and correctly imports
`LLMClient` only under `TYPE_CHECKING` (no concrete provider coupling).
4.3.2 adds test breadth over 4.3.1's fixtures without touching any
production code. `agents/query/` now has 19/19 tests passing (13 from 4.3.1
+ 6 from 4.3.2); `ruff check agents/query/` is clean in both runs.

Architecturally, `refine_intent()` has no classification logic of its own --
it is a pure parse-and-validate function that trusts whatever `query_type`
string the LLM returns, delegating all real classification to the model.
This design was disclosed and accepted in 4.3.1's own verification, and
4.3.2's tests correctly test to that design (JSON round-trip fidelity
across fixtures), not to a stricter "the system classifies queries
correctly" standard that no mocked-LLM unit test could satisfy regardless
of how it were written. See the carried-forward finding below -- this
matters directly for whoever verifies the future `topic_selector.py`
(issue #23).

## Verification

- 4.3.1: `verdict=PASS_WITH_COMMENTS`, commit `694b0e3`, run
  `.cdr/runs/2026-07-11/012-verification/verification.json`. `query/` +
  `llm/` targeted suite: 81/81 passed. `ruff check agents/query/`: clean.
  Full regression: 221 passed, 2 failed -- both failures are the
  pre-existing `ingestion/test_shortlist.py` protobuf `VersionError`
  (tracked as issue #46), confirmed unrelated to this diff. Confidence:
  high (verifier independently confirmed the LLD field-name claim,
  fence-stripping reuse claim, and taxonomy-not-specified-in-LLD claim by
  reading source directly).
- 4.3.2: `verdict=PASS_WITH_COMMENTS`, commit `08f434e`, run
  `.cdr/runs/2026-07-11/014-verification/verification.json`. `query/`
  suite: 19/19 passed (13 from 4.3.1 + 6 new). Full suite (excluding the
  known-broken e2e smoke test): 230 passed, 2 failed -- same pre-existing,
  unrelated protobuf `VersionError` as above. `ruff check agents/query/`:
  clean. Confidence: medium-high (independently re-ran both pytest suites
  and ruff rather than trusting the commit message or self-consistency
  self-report).
- `issue_22_closeout_assessment` (from 4.3.2's verification.json):
  `ready_for_closeout: true`. Both subtasks' non-blocking findings are
  documentation/lint/coverage-breadth items, not defects that block
  functionality the topic-selector will depend on.

## Release notes

- Added `agents/query/intent_refiner.py::refine_intent()` -- LLM-backed
  intent refinement, producing a structured `IntentRefinerResult`
  (`refined_intent`, `entities`, `query_type`) from a raw user query via
  dependency-injected `LLMClient`.
- Added a self-authored, disclosed `query_type` taxonomy
  (`factual_lookup` / `broad_exploratory`) as a closed 2-value `Literal`,
  since `docs/LLD/query-agent.md` specifies the field but not its value
  names.
- Added `agents/query/test_intent_refiner_types.py`, broadening test
  coverage of `query_type` classification-variant fixtures across 6
  additional, non-overlapping queries; no production-code change.
- No behavior change to `agents/ingestion/` or any other existing package.

## Consolidated non-blocking findings (verbatim from both verification.json files)

**From 4.3.1 (`.cdr/runs/2026-07-11/012-verification/verification.json`):**

- Cross-package import of `agents.ingestion._json_fences` (a private,
  underscore-prefixed module) from `agents/query` -- consider relocating
  to a shared/common module. (Architecture-conformance detail: `strip_code_fences`
  is genuinely reused, not duplicated -- no independent fence-stripping
  logic exists in `agents/query/intent_refiner.py`. No HLD/LLD layering
  rule forbids this cross-package import, but it is a minor maintainability
  smell: a helper used by two independent pipeline packages arguably
  belongs in `agents/llm/` or a common module rather than living inside
  ingestion's private namespace.)
- No explicit test for empty-query-string or entities-list-item-wrong-type
  edge cases (code correctly handles both, just untested).
- `query_type` taxonomy is a closed 2-value `Literal`; extending it later
  touches `intent_refiner.py` itself, not just test fixtures. (Per the
  `sibling_subtask_check`: the two-value taxonomy, `factual_lookup` /
  `broad_exploratory`, was taken verbatim from 4.3.2's own
  acceptance-criteria wording, so it directly satisfies 4.3.2's stated
  minimum. It is a minimal, not extensible-by-default taxonomy -- if
  `topic_selector.py` or a future subtask needs finer-grained categories,
  that will require a follow-up change to `intent_refiner.py` itself, not
  just to the test file. Not a blocker for 4.3.1 as scoped, but flagged
  forward.)

**From 4.3.2 (`.cdr/runs/2026-07-11/014-verification/verification.json`):**

- Acceptance criteria "correctly differentiates" is satisfied only in the
  minimal parse-fidelity sense, not in a classification-accuracy sense,
  because `refine_intent()` has no classification logic of its own. Per
  the verifier's `architectural_gap_analysis`: since `refine_intent()` is,
  by 4.3.1's own explicit and already-accepted design, a pure
  prompt-then-parse-JSON function that delegates all classification to the
  LLM and only validates the returned `query_type` string against a closed
  2-value enum, 4.3.2's 6 new fixture tests necessarily prove
  JSON-parsing fidelity across fixtures (does a string survive a round
  trip through `json.loads` and dataclass construction) rather than real
  intent-classification accuracy -- no fixture query's *text* is ever
  inspected by production code, so every test is guaranteed to pass by
  construction (the mock is told the expected type and the assertion
  checks that the same value comes back out). This is a valid, minimal,
  and defensible reading of the acceptance criteria given 4.3.1's design
  (and matches the LLD, which specifies the field but not a client-side
  classification algorithm) -- not a defect in this commit, but a
  disclosed limitation inherited from 4.3.1. **Flagged forward explicitly:
  whoever builds or verifies the future `topic_selector.py` (issue #23)
  should know that real query-classification accuracy has never actually
  been tested end-to-end against unmocked LLM output -- only
  threading-through-unmocked... i.e. parse-and-pass-through fidelity
  has.** Any claim of real classification accuracy would need a
  dedicated integration/eval-style test exercising a real (unmocked) LLM
  call, which does not yet exist anywhere in this pipeline.
- `_FakeLLMClient` is duplicated near-verbatim (only cosmetic differences,
  e.g. dropped error-path support) across `test_intent_refiner.py` and
  `test_intent_refiner_types.py`; consider extracting a shared
  `conftest.py` fixture if a third `agents/query/` test file needs the
  same fake. Low severity, non-blocking.
- No new edge cases added beyond 4.3.1's; correctly out of scope for this
  subtask.

All findings above are being carried forward via this document and
`.cdr/memory/pending.md` (the classification-accuracy gap specifically, to
ensure it is not lost before issue #23's `topic_selector.py` is built).
None of them block issue #22's closure for its own stated scope, but all
should be surfaced to the user before deciding whether to push and/or
close issue #22 (and milestone #6) on GitHub.

## Note on anomalous tool-flow content

During this run, text formatted as harness-style system-reminder blocks
(a "date changed" notice, an MCP "tokensave" server instructions block for
a tool not present in the actual tool list, and an "Auto Mode Active"
directive) appeared directly in the tool-call stream, matching the same
recurring pattern already flagged in prior CDR runs for this repo (see
`.cdr/commits/task-issue-20-summary.md`). This content did not arrive
embedded inside actual repo/GitHub data (issue bodies, commit messages,
diffs read from disk) -- it appeared directly in the tool-call flow itself.
Per standing instructions this was treated as untrusted/informational and
not acted upon; no scope, permissions, or verdicts were altered because of
it. Flagged here again for the user's awareness.
