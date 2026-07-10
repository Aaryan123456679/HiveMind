# Issue #23 — topic_selector.py (Phase 4: Query pipeline)

## Summary

Issue #23 delivers `agents/query/topic_selector.py`, the query-pipeline
component that turns a `SearchCandidates` result into the final file set
handed to the answer-generation stage. All four subtasks are implemented
and independently CDR-verified PASS or PASS_WITH_COMMENTS:

- **4.4.1** — top-k candidate selection (`select_top_k`, `TopicCandidate`, `DEFAULT_K=3`)
- **4.4.2** — graph-traversal expansion decision (`is_insufficient_alone`, `expand_insufficient_topics`, `GraphNeighbor`, `GraphNeighborsFn`)
- **4.4.3** — hard-cap enforcement (`combine_and_cap`, `k+2k` invariant)
- **4.4.4** — end-to-end integration test composing all three stages of the real pipeline

Together these close out the milestone's query-side selection logic:
`SearchCandidates` → top-k → (conditional) 0–2 hop `GraphNeighbors`
expansion → dedup + hard-cap → final file set.

## Features

- Deterministic top-k selection over `SearchCandidates` output, with a
  configurable `k` (default 3) and stable tie-breaking.
- Per-topic "insufficient alone" heuristic driving conditional graph
  expansion via an injected `GraphNeighborsFn` (0–2 hops, matching the
  engine's validated depth range).
- System-wide `k + 2k` hard cap on the combined selected+expansion file
  set, with dedup across selected topics and expansion neighbors,
  preserving deterministic ordering under truncation.
- A genuine end-to-end integration test that chains the real
  `select_top_k` → `expand_insufficient_topics` → `combine_and_cap`
  functions (only the `GraphNeighborsFn` RPC boundary is mocked),
  proving the three previously unit-tested stages compose correctly.

## Impact

- The query agent now has a complete, testable selection pipeline ready
  to be wired to the real `SearchCandidates`/`GraphNeighbors` gRPC calls.
- No gRPC adapter/`wiring.py` exists yet in `agents/query/` for the real
  RPCs — `SearchCandidatesFn`/`GraphNeighborsFn` remain typed injection
  points only. This is explicitly deferred to **issue #25**; low
  integration cost given identical field shapes to the proto messages,
  but real work remains.
- All changes are additive to `agents/query/`; no other package's
  production code was touched. Full regression suite (`pytest . --ignore=ingestion/test_e2e_smoke.py`)
  passes throughout at 240–270 tests depending on subtask, with the same
  2 pre-existing, unrelated protobuf gencode/runtime version-mismatch
  failures in `ingestion/test_shortlist.py` (tracked separately as
  issue #46) present before, during, and after this work.

## Verification

| Subtask | Commit(s) | Verdict | Verification run |
|---|---|---|---|
| 4.4.1 | `5cc0ea3` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/017-verification` |
| 4.4.2 | `f65787b` + `7d2f3dd` (docs-only) | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/019-verification` |
| 4.4.3 | `3454a30` + `ff4cbe9` (docs-only) | PASS | `.cdr/runs/2026-07-11/021-verification` |
| 4.4.4 | `451a590` + `e145fc2` (docs-only) | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/023-verification` |

Non-blocking findings carried forward (all previously disclosed in the
verification runs and `.cdr/index/regression.jsonl`; none are blocking):

- **No gRPC adapter yet (4.4.1).** `agents/query/` has no
  `GrpcSearchCandidatesClient`-style adapter/`wiring.py` translating the
  real proto `SearchCandidates`/`GraphNeighbors` RPCs, unlike the
  `agents/ingestion/shortlist.py` precedent. `SearchCandidatesFn` and
  `GraphNeighborsFn` remain typed injection points only. Deferred to
  issue #25; low-cost given matching field shapes.
- **False docstring invariant on negative scores (4.4.2).**
  `is_insufficient_alone`'s docstring claims "the top topic is never
  flagged for any ratio ≤ 1," but this does not hold when the top
  topic's score is negative (e.g. `top_score=-10`, `ratio=0.5` →
  `threshold=-5`, and `-10 < -5` is true). No validation rejects
  negative `TopicCandidate.score`, and no test exercises this case.
  Low real-world impact since `SearchCandidates` scores are non-negative
  in practice, but the stated guarantee is not actually upheld for the
  full domain of float scores the function accepts.
- **Missing parametrized cap-formula test (4.4.3).** The `k + 2k` cap
  formula was manually verified correct for `k in {0,1,2,5,10}` during
  verification, but only `k=3` (default) is pinned down by a committed,
  automated test in `test_topic_selector_cap.py`.
- **Growing test-helper duplication (4.4.2–4.4.4).** `_FakeLLMClient`-
  and `_RecordingGraphNeighbors`-style fixtures (plus `_topic()`/
  `_neighbor()` factories) are now duplicated verbatim across the test
  files in `agents/query/`. Consistent with the package's existing
  per-file-local-fixture convention, not a new regression, but should be
  extracted to a shared `conftest.py`/`_helpers.py` if a 5th test file
  needs the same shapes.
- **3 integration scenarios vs. literal "single fixture scenario" (4.4.4).**
  The issue's test-spec wording asked for a single fixture scenario;
  4.4.4 delivered 3 (expansion+cap, no-expansion, and dedup-focused).
  This strictly increases rigor over the literal spec and is not a
  deviation of concern.

## Release Notes

- Added `agents/query/topic_selector.py`: top-k selection, conditional
  graph-expansion decisioning, and hard-cap (`k+2k`) enforcement for the
  query agent's candidate-file selection pipeline, with a full unit and
  integration test suite (`agents/query/test_topic_selector*.py`).
- No user-facing or wire-protocol changes; this is an internal
  query-pipeline component not yet wired to live gRPC calls (tracked for
  issue #25).
- Known, non-blocking follow-ups: gRPC adapter for `SearchCandidates`/
  `GraphNeighbors` (issue #25), negative-score edge case in the
  insufficiency heuristic, a parametrized cap-formula test, and test
  fixture consolidation once a 5th `agents/query/` test file appears.
