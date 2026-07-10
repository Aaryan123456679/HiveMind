# Requirement -- Issue #24 subtask 4.5.2

**Issue:** #24 "[4] synthesizer.py (agents/query/)" -- milestone #6 "Phase 4: Query pipeline".
This is issue #24's LAST subtask (4.5.1 already implemented, verified PASS_WITH_COMMENTS,
committed as `c8c49cf`, not pushed).

## Subtask 4.5.2 -- Citation-format validation test

**Acceptance criteria:** Any citation in the synthesized answer must reference a file path
that was actually present in the selected-file input set; citations to unknown paths are
flagged/rejected.

**Test spec:** `pytest agents/query/test_synthesizer_citations.py`: fixture LLM response
containing one valid and one hallucinated citation, assert the hallucinated one is
detected/flagged.

**Impacted modules (disclosed):** `agents/query/test_synthesizer_citations.py` only.

## Gap analysis: is a synthesizer.py change needed?

Per 4.5.1's own module docstring and `SynthesizerResult.unknown_citations()` docstring, that
method was built explicitly as "a defensible building block for subtask 4.5.2's dedicated
citation-format validation (hallucinated-citation detection/rejection)": it returns the
order-preserved, deduplicated subset of `citations` not present in `provided_paths`.

Read `synthesizer.py` and `test_synthesizer.py` in full (current HEAD `c8c49cf`) before
deciding. Findings:

- `unknown_citations()` already correctly implements the "flagged" half of "flagged/rejected"
  for the *general* case (see `test_synthesizer.py::test_unknown_citations_flags_path_not_in_provided_set`,
  which already covers one valid + one hallucinated citation, and
  `test_unknown_citations_deduplicated_and_order_preserved`).
- `synthesize_answer()` itself never raises/rejects on an unknown citation -- by explicit,
  documented design of 4.5.1 ("this module ... deliberately does not itself raise/reject on
  a hallucinated citation").
- The acceptance criteria says "flagged/rejected" (disjunctive), and the test spec says
  "detected/flagged" (disjunctive) -- not "must raise an exception end-to-end". Detection via
  a callable, correctly-behaving method that a caller can inspect satisfies "flagged":
  callers get a definitive, correct answer to "which citations are hallucinated?" without
  the module force-choosing between silently dropping the answer, mutating it, or raising
  (any of which would be a bigger, unrequested behavioral commitment on top of 4.5.1's
  already-settled, disclosed design).
- 4.5.2's own Impacted Modules list names only the test file -- consistent with "no
  synthesizer.py change is the default expectation" for this subtask.

**Decision: no synthesizer.py change is needed.** The acceptance criteria are already fully
satisfiable by testing `unknown_citations()` as-is, in the specific end-to-end scenario the
acceptance criteria describes (LLMClient-mocked `synthesize_answer()` call whose fixture
response contains one valid + one hallucinated citation), via a new dedicated test file. This
avoids restructuring or renaming any 4.5.1 API surface, and matches the subtask's own
disclosed Impacted Modules list.

The one thing 4.5.1's existing test file does *not* yet do that 4.5.2's test spec explicitly
asks for: a *dedicated* file (`test_synthesizer_citations.py`) whose fixture LLM response
contains exactly one valid and one hallucinated citation embedded in realistic answer prose
(mirroring the LLMClient-mocked-fixture pattern), asserting detection. That is this run's
actual deliverable.
