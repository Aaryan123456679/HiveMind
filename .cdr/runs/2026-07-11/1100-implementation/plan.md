# Plan

## Architecture discovery
- `agents/ingestion/wiring.py::execute_segment`: fail-fast before `PutSegment`
  (`TopicNotFoundError` for unresolvable `APPEND_EXISTING.target_topic`), best-effort
  with error collection after (`related_topics` resolution failures collected into
  `errors`, no raise, edge simply skipped).
- `agents/ingestion/test_e2e_smoke.py::test_full_pipeline_smoke`: live smoke test
  (real Ollama + real `engine/cmd/smokeserver` subprocess), assertion 3 currently only
  checks `create_new_count + append_existing_count == successful_docs` and
  `create_new_count >= 1` -- no lower bound on `append_existing_count`.
- `agents/ingestion/test_segment_wiring.py`: pre-existing
  `test_unresolvable_related_topic_collected_not_raised` already calls
  `execute_segment` directly but mixes one resolvable + one unresolvable
  `related_topic` in the same segment -- not an isolated single-branch test.

## Impact analysis
- F1 change is test-only, single assertion addition in `test_e2e_smoke.py`; no
  production code touched. Risk: if the live model/engine combination genuinely never
  produces an append in a given run, this could make the (already-skipped-unless-live)
  smoke test flaky. Mitigated by module's own documented issue #43 fix making later
  documents' `shortlist()` calls surface earlier CREATE_NEW topics, and by 11
  real documents being processed (multiple chances). Accepted per explicit issue
  instruction (F1 acceptance criteria).
- F2 change is a new, additive unit test in `test_segment_wiring.py`; no changes to
  existing tests, no production code touched. Zero regression risk.

## Steps
1. Read `wiring.py`'s `execute_segment` and current `test_e2e_smoke.py` assertions
   (post-2288388) -- done.
2. F1: add `assert append_existing_count >= 1` (with descriptive failure message) to
   `test_full_pipeline_smoke`, plus a module-docstring note explaining the
   strengthening and citing 4.5.18.6.
3. F2: add `test_execute_segment_related_topic_with_no_matching_file_isolated` to
   `test_segment_wiring.py`, directly unit-testing `execute_segment` with a single
   unresolvable `related_topic` (no entities, no other related topics) -- isolating
   the branch instead of mixing it with a resolvable topic as the pre-existing test
   does.
4. Run `pytest agents/ingestion/test_segment_wiring.py -q` (must be green, unmocked
   local unit tests, no network/engine dependency).
5. Attempt `pytest agents/ingestion/test_e2e_smoke.py::test_full_pipeline_smoke`;
   disclose actual reachability of `go`/`grpc` stubs/Ollama in this sandbox rather
   than fake a pass.
6. One local commit (Problem/Solution/Impact), no push.
7. Write handoff.json (pointers only) and hand off to `/cdr:verify --subtask 4.5.18.6`.
