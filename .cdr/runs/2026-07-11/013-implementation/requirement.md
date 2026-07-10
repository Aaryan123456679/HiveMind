# Requirement -- Issue #22 subtask 4.3.2

**Issue:** #22 `[4] intent_refiner.py (agents/query/)`, milestone #6 "Phase 4: Query pipeline".
**Subtask:** 4.3.2 -- Unit tests covering `query_type` variants (issue #22's LAST subtask).

## Acceptance criteria (verbatim from issue)
> The refiner correctly differentiates at least the `query_type` categories the
> topic-selector depends on (e.g. factual/lookup vs. broad/exploratory), verified
> across multiple fixture queries.

## Test spec (verbatim from issue)
> pytest `agents/query/test_intent_refiner_types.py`: assert correct `query_type`
> classification per fixture, with mocked LLM responses covering each category.

## Impacted modules
- `agents/query/test_intent_refiner_types.py` (new file only)

## Scope boundary
- Subtask 4.3.1 (`agents/query/intent_refiner.py`, `refine_intent()`, `QueryType`)
  is already implemented, verified PASS_WITH_COMMENTS, and committed locally at
  `694b0e3`. It is NOT to be modified by this run.
- `agents/query/test_intent_refiner.py` (4.3.1's own test file) already covers
  *output shape* (one factual_lookup fixture, one broad_exploratory fixture, plus
  malformed-output/error cases) and explicitly defers "`query_type` classification
  *accuracy* across many fixture variants" to this subtask (4.3.2) per its own
  docstring (line 12-15).
- This run's job: add `agents/query/test_intent_refiner_types.py` containing
  *additional*, non-duplicative fixture queries across BOTH `QueryType` values
  (`factual_lookup`, `broad_exploratory`), each proven to classify correctly
  through `refine_intent()` when the mocked `LLMClient` returns the expected
  `query_type` for that fixture -- i.e. prove the differentiation the
  acceptance criteria asks for, using multiple *distinct* representative queries
  per category (not just the two already covered in 4.3.1's file).
- No production code changes. No changes to `intent_refiner.py`.

## Definition of done
- New file `agents/query/test_intent_refiner_types.py` only.
- `cd agents && python3 -m pytest query/ -q` passes.
- `cd agents && python3 -m pytest . --ignore=ingestion/test_e2e_smoke.py -q` passes
  modulo the known pre-existing issue #46 protobuf collection error in
  `ingestion/test_shortlist.py` (unrelated, not touched).
- `ruff check` clean on the new file.
- One local commit (Problem/Solution/Impact style), no push.
