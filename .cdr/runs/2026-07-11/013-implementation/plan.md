# Plan -- 4.3.2

1. Create `agents/query/test_intent_refiner_types.py`:
   - Module docstring explaining scope: query_type classification-accuracy coverage
     per 4.3.2's acceptance criteria, distinct from 4.3.1's shape-only fixtures.
   - Reuse `_FakeLLMClient(LLMClient)` (copied inline, same pattern as
     `test_intent_refiner.py`, since no shared conftest/helpers module exists in
     `agents/query/` to import from -- keeps this file self-contained, matching
     repo convention of one fake-client-per-test-file, e.g.
     `ingestion/test_segment.py`).
   - Reuse a `_well_formed_json(...)` helper (same shape) to build canned LLM
     responses.
   - Define a parametrized fixture table with >= 3 distinct queries classified
     `factual_lookup` and >= 3 distinct queries classified `broad_exploratory`,
     none overlapping with 4.3.1's two fixtures ("what's the total on invoice
     4521?" / "tell me about our billing disputes").
     - factual_lookup fixtures: "What is the capital of France?", "Who wrote the
       novel 1984?", "What year did the Berlin Wall fall?"
     - broad_exploratory fixtures: "Tell me everything about the history of the
       Roman Empire", "Give me an overview of our company's product roadmap",
       "Summarize all the research on climate change adaptation strategies"
   - One `@pytest.mark.parametrize` test asserting `refine_intent(...).query_type
     == expected_type` for each of the 6 fixtures, with the mocked LLM response's
     `query_type` field set to `expected_type` for each.
   - One additional non-parametrized test that runs a factual_lookup fixture and
     a broad_exploratory fixture back-to-back (two separate `refine_intent` calls
     with two separate fake clients) and asserts the two results differ
     (`result_a.query_type != result_b.query_type`) -- a direct assertion of
     "differentiation" per the acceptance criteria's own wording, not just two
     independent equality checks.
   - Keep entities/refined_intent content plausible per fixture but do not assert
     on them (out of scope; that's 4.3.1's job) beyond a light sanity check that
     `IntentRefinerResult` is returned.
2. Run `cd agents && python3 -m pytest query/ -q`.
3. Run `cd agents && python3 -m pytest . --ignore=ingestion/test_e2e_smoke.py -q`
   and confirm the only failures are the pre-existing issue #46 protobuf
   collection errors in `ingestion/test_shortlist.py` (unrelated).
4. Run `ruff check agents/query/test_intent_refiner_types.py` (and full `ruff check`
   for the diff).
5. Write validation-matrix.json mapping each planned test to the acceptance
   criteria / test spec line it satisfies.
6. Write self-consistency.json (internal sanity only, not verification).
7. One local commit, Problem/Solution/Impact style, no push.
8. Write handoff.json with pointers only.
