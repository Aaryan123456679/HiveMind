# Issue #24 — synthesizer.py (Phase 4: Query pipeline)

## Summary

Issue #24 delivers `agents/query/synthesizer.py`, the final stage of the
query pipeline's LLM-facing surface: turning a refined intent plus the
selected/expanded markdown files (from 4.3.x/4.4.x) into a citation-annotated
answer, with a mechanism to detect citations to files that were not actually
in the selected-file input set (hallucinated citations). Both subtasks are
implemented and independently CDR-verified **PASS_WITH_COMMENTS**:

- **4.5.1** — prompt assembly + citation-annotated answer generation
  (`synthesize_answer()`, `SynthesizerResult`, `unknown_citations()`)
- **4.5.2** — dedicated test proving hallucinated-citation detection
  (`test_synthesizer_citations.py`)

Together these close out **issue #24**, which is the last open issue under
**milestone #6 "Phase 4: Query pipeline"**. With `intent_refiner.py` (4.3.x),
`topic_selector.py` (4.4.x), and now `synthesizer.py` (4.5.x) all implemented
and verified, every module named in `docs/LLD/query-agent.md` exists at the
local-commit level.

## Features

- `synthesize_answer()`: assembles a prompt from the refined intent and the
  selected markdown (concatenated with `"## File: <path>"` headers), calls
  the injected `LLMClient`, and parses a JSON response into a
  `SynthesizerResult` containing the answer prose (with inline `[<path>]`
  citations) and a flat `citations` list — following `intent_refiner.py`'s
  established conventions (`TYPE_CHECKING`-only DI, prompt-then-parse-JSON,
  frozen dataclass result, `SynthesizerError`/`SynthesizerParseError` pair).
- `SynthesizerResult.unknown_citations()`: compares the result's citations
  against the actual set of selected-file paths and returns any citation not
  present in that set — the building block for hallucination detection.
- A dedicated test file (`test_synthesizer_citations.py`) exercising
  `synthesize_answer()` end-to-end with a fixture LLM response containing
  exactly one valid and one hallucinated citation (in both orderings),
  asserting exact-value (not just truthy) detection of the hallucinated
  citation while leaving the raw `citations` list untouched (flagging is
  additive, not a silent filter).

## Impact

- `agents/query/` now has all three LLD-named modules implemented
  (`intent_refiner`, `topic_selector`, `synthesizer`), completing the
  query-pipeline's LLM-facing surface end to end: refined intent → top-k/
  graph-expanded file selection → citation-annotated synthesized answer with
  hallucination detection.
- `synthesizer.py`'s wire format for the LLM response (`answer` +
  `citations` JSON fields) and the `"## File: <path>"` header syntax were
  disclosed implementation choices, since the LLD names neither explicitly —
  future consumers of this module should treat these as the established
  contract.
- No production code outside `agents/query/` was touched by either subtask.
  4.5.2 is test-only and confirmed (via direct diff, not just commit-message
  claim) to leave `synthesizer.py` byte-for-byte unchanged from 4.5.1.
- Full regression suite (`pytest . --ignore=ingestion/test_e2e_smoke.py`)
  passes at 284–288 tests across both subtasks, with the same 2
  pre-existing, unrelated protobuf gencode/runtime version-mismatch failures
  in `ingestion/test_shortlist.py` (tracked separately as issue #46) present
  before, during, and after this work. `ruff check agents/query/` clean
  throughout.
- Both `c8c49cf` and `b8da449` commit messages already follow the
  Problem/Solution/Impact standard — no deviation to note, no git history
  rewrite needed.
- Consistent with issues #20–#23 precedent: both commits are local-only
  (not pushed), and this issue is not being closed on GitHub as part of this
  step — a separate batched push/close step will handle that.

## Verification

| Subtask | Commit | Verdict | Verification run |
|---|---|---|---|
| 4.5.1 | `c8c49cf` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/026-verification` |
| 4.5.2 | `b8da449` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/028-verification` |

Non-blocking findings carried forward (all previously disclosed in the
verification runs and `.cdr/index/regression.jsonl`; none are blocking):

- **F-4.5.1-1 — `citations` "deduplicated" docstring not code-enforced.**
  `SynthesizerResult.citations`'s docstring claims a "flat, deduplicated
  list," but `_parse_synthesis_json` does not enforce dedup on the LLM's raw
  JSON `citations` field — it's a straight pass-through trusting the
  prompt's own dedup instruction to the model. No test exercises an LLM
  response with duplicate citations. `unknown_citations()` (the actual
  4.5.2 building block) does its own internal dedup correctly regardless,
  so this does not affect 4.5.2's use, but the docstring's guarantee about
  `SynthesizerResult.citations` itself is not code-enforced.
- **F-4.5.1-2 — exact-match citation semantics, no path normalization.**
  `unknown_citations()` uses exact string/set-membership matching against
  provided paths. Adversarially tested with prefix/substring, trailing-slash,
  and case-differing variants — all correctly flagged as unknown (no false
  negatives). The inverse risk (a legitimate citation differing from its
  header only by formatting being falsely flagged) is a theoretical
  false-positive risk, acceptable given the single documented
  `"## File: <path>"` header format currently in use.
- **F-4.5.2-1 — thin net-new test contribution vs. 4.5.1.** 3 of the 4
  tests in `test_synthesizer_citations.py` substantially duplicate the
  scenario and literal fixture path strings (`billing/InvoiceDisputes.md`,
  `legal/InternalMemo.md`) of the pre-existing
  `test_synthesizer.py::test_unknown_citations_flags_path_not_in_provided_set`
  (4.5.1). Only `test_hallucinated_citation_flagged_regardless_of_position`
  adds genuinely new coverage (order-independence). Acceptance criteria are
  satisfied literally (a dedicated file exists at the named path with the
  named scenario), but the file's net-new contribution beyond 4.5.1 is thin.
- **F-4.5.2-2 — uncommitted adversarial edge-case coverage.** Case-different,
  trailing-slash, backslash-separator, empty-list, all-hallucinated, and
  co-occurring-near-miss variants were all independently verified correct
  against `unknown_citations()`'s exact-match design (consistent with
  F-4.5.1-2), but none of these are committed as regression-protecting
  tests.
- **F-4.5.2-3 — prompt-injection attempt disclosed during verification.**
  The 4.5.2 verification session's own tool-output stream contained
  injected fake system-reminder-style text (a fake date-change notice, a
  fake MCP "tokensave" tool-instruction block for a nonexistent tool, and a
  fake "Auto Mode Active" directive) unrelated to any gh/git content
  actually examined. None were acted upon; no such text was found in GitHub
  issue #24's actual body or in commits `b8da449`/`c8c49cf` themselves. (A
  similar injection attempt recurred during this CDR-commit step and was
  likewise disclosed and not acted upon.)

## Release Notes

- Added `agents/query/synthesizer.py`: prompt assembly and citation-annotated
  answer synthesis (`synthesize_answer()`, `SynthesizerResult`), completing
  the query pipeline's three LLD-named LLM-facing modules
  (`intent_refiner`, `topic_selector`, `synthesizer`).
- Added `SynthesizerResult.unknown_citations()` and a dedicated test file
  (`agents/query/test_synthesizer_citations.py`) proving detection of
  citations to files absent from the selected-file input set
  (hallucinated citations).
- No user-facing or wire-protocol changes beyond the internal LLM
  response contract (`answer` + `citations` JSON fields, `"## File: <path>"`
  headers) established as part of this work.
- This closes issue #24, the last issue under milestone #6 "Phase 4: Query
  pipeline" — Phase 4 is functionally complete at the local-commit level.
- Known, non-blocking follow-ups: code-enforce (or soften the docstring on)
  citation dedup, consider path normalization for citation matching,
  consider committing the adversarial edge-case variants as regression
  tests, and consider consolidating the growing `_FakeLLMClient`-style test
  fixture duplication across `agents/query/` test files once a 5th consumer
  appears.
