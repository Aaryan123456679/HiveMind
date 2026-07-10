# Plan — 4.4.4 integration test

## New file: `agents/query/test_topic_selector_integration.py`

Module docstring: cites issue #23 subtask 4.4.4's test spec verbatim, states this file
composes the three real pipeline functions with only `GraphNeighborsFn` mocked, and lists
the three required scenarios.

### Shared fixtures/helpers
- Reuse `_RecordingGraphNeighbors` (copied from `test_topic_selector_expansion.py`'s
  established shape: records `(file_id, hops)` call tuples, returns a canned per-file_id
  neighbor list from a dict, defaults to `[]` for unknown file_ids) — kept local to this
  file since there's no shared conftest/helpers module in `agents/query/` today, matching
  the "no premature shared-fixture module" pattern established by the other three test
  files each defining their own local fixtures.
- Small `_topic(file_id, score, path=None)` factory mirroring `test_topic_selector_cap.py`'s
  `_topic()` helper.

### Scenario 1 — expansion triggers, k+2k cap enforced end-to-end
- 5 candidates, unsorted scores, one clear top scorer and one weak scorer whose score is
  `< 0.5 * top_score` after `select_top_k(candidates, k=3)` picks it into the top 3 (so
  the *insufficiency* is evaluated against the k=3 selection's own top score, not the
  full candidate pool's top score — must construct scores carefully so a weak-but-still-
  top-3 topic exists).
- `select_top_k(candidates, k=3)` -> assert exact top-3 file_ids/order.
- `expand_insufficient_topics(selected, mock, ratio=DEFAULT_INSUFFICIENCY_RATIO)` -> assert
  mock called exactly once, only for the weak topic, with `hops=DEFAULT_EXPANSION_HOPS`.
  Mock returns > 2k distinct neighbor file_ids (oversized) to exercise the cap.
- `combine_and_cap(selected, expansions, k=3)` -> assert `len(result) == 9` (k+2k),
  all 3 selected file_ids present (priority), remaining slots filled by neighbors in
  order, nothing beyond cap.

### Scenario 2 — no insufficient topic -> zero expansion calls
- 3 candidates all within `ratio * top_score` of each other (e.g. scores 0.9, 0.85, 0.8;
  none below `0.5 * 0.9 = 0.45`).
- `select_top_k(candidates, k=3)` -> 3 selected.
- `expand_insufficient_topics(selected, mock)` -> assert `results == []` and
  `mock.calls == []` (proves no RPC-shaped call happens at all, not just that its
  results are discarded).
- `combine_and_cap(selected, [], k=3)` -> result is exactly the 3 selected file_ids,
  under the cap, no dedup needed — confirms the "no expansion" path still composes
  cleanly through `combine_and_cap`.

### Scenario 3 — dedup-across-expansion surfaces through the full pipeline
- 4 candidates, `k=3`, two of the three selected topics are flagged insufficient, and
  their two independent `GraphNeighbors` mock responses deliberately share one common
  neighbor file_id (and one of the neighbor file_ids also collides with a *directly
  selected* topic's own file_id, to also exercise the selected/expansion-overlap dedup
  case end-to-end, not just neighbor/neighbor overlap).
- Run all three real functions in sequence.
- Assert: both insufficient topics triggered exactly one call each (mock.calls length 2,
  correct file_ids/hops); the shared neighbor file_id appears exactly once in the final
  `combine_and_cap` output; the selected/neighbor-colliding file_id also appears exactly
  once; final length equals the number of *distinct* file_ids across selected+neighbors
  (under the k+2k cap in this scenario, so cap truncation is not the reason for the
  count — isolating dedup as the mechanism being proven, distinct from Scenario 1's cap
  proof).

## Non-goals
- Not testing `topic_selector.py` unit-level edge cases (negative k, ratio validation,
  etc.) — already covered by the three existing unit test files; this file only proves
  composition.
- Not adding a shared conftest.py/fixtures module — three scenarios don't yet justify the
  indirection, matching this package's existing per-file-local-fixture convention.

## Validation
- `cd agents && python3 -m pytest query/ -q`
- `cd agents && python3 -m pytest . --ignore=ingestion/test_e2e_smoke.py -q` (regression)
- `cd agents && ruff check query/test_topic_selector_integration.py` (and full `ruff check`)
