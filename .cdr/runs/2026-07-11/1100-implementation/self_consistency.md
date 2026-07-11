# Self-consistency (internal sanity only -- not verification)

- `python3 -m py_compile agents/ingestion/test_e2e_smoke.py agents/ingestion/test_segment_wiring.py` -> OK.
- `pytest agents/ingestion/test_segment_wiring.py -q` -> 23 passed (was 22 before this
  change; the +1 is the new `test_execute_segment_related_topic_with_no_matching_file_isolated`).
- `pytest agents/ingestion/ -q --ignore=ingestion/test_e2e_smoke.py` -> 160 passed,
  2 failed (both pre-existing, in `test_shortlist.py`, same protobuf
  gencode/runtime `VersionError` as the smoke test -- confirmed pre-existing via
  `git stash` reproducing the identical failure on unmodified HEAD; not caused by
  this subtask's changes, not in this subtask's impacted-modules list).
- `pytest agents/ingestion/test_e2e_smoke.py::test_full_pipeline_smoke` (the
  requested live spec) -- could not execute: collection-time `VersionError` from the
  environment's protobuf gencode/runtime mismatch, confirmed pre-existing via the
  same `git stash` comparison. Disclosed explicitly in `validation_matrix.md`; not
  faked.
- Matrix coverage: both F1 and F2 requirements have a corresponding change and
  (for F2) a green, executed test. F1's assertion could not be executed live in this
  sandbox for reasons unrelated to its own correctness (see above), but was
  code-reviewed against the module's documented pipeline semantics.

This is internal sanity checking only, per invariant I4 -- independent verification
is deferred to `/cdr:verify --subtask 4.5.18.6`, not performed here.
