# Validation matrix

| # | Requirement | Covered by | Status |
|---|---|---|---|
| F1 | `test_full_pipeline_smoke` hard-requires `append_existing_count >= 1` | `agents/ingestion/test_e2e_smoke.py` new `assert append_existing_count >= 1` block (after existing assertion 3) | Implemented. Not executable in this sandbox (see limitation below) -- code-reviewed against `execute_segment`'s documented resolution semantics and the existing 11-real-document smoke run shape. |
| F2 | New, dedicated unit test isolating `execute_segment`'s fileID-resolution branch for an unresolvable `related_topic`, distinct from pre-existing coverage | `agents/ingestion/test_segment_wiring.py::test_execute_segment_related_topic_with_no_matching_file_isolated` | Green (`pytest agents/ingestion/test_segment_wiring.py -q` -> 23 passed, was 22). |
| Regression | Existing `test_segment_wiring.py` coverage still green | Full file run | Green, 23 passed (22 pre-existing + 1 new). |
| Live smoke test | `pytest agents/ingestion/test_e2e_smoke.py::test_full_pipeline_smoke` | Attempted | **Could not run** -- see "Sandbox limitation" below. |

## Sandbox limitation (disclosed, not faked)

`go` toolchain and a reachable local Ollama server (port 11434, HTTP 200) are both
present in this sandbox. However, `agents/ingestion/test_e2e_smoke.py`'s own
`_grpc_stubs_available()` skip-guard import of the generated `hivemind_pb2` module
raises `google.protobuf.runtime_version.VersionError: Detected incompatible
Protobuf Gencode/Runtime versions ... gencode 6.33.5 runtime 5.29.6` -- an
uncaught exception (not an `ImportError`), so pytest fails at **collection time**
with `!!!!!!!!!!!!!!!!!!!! Interrupted: 1 error during collection !!!!!!!!!!!!!!!!!!!!`
rather than cleanly skipping the module.

This was confirmed to be **pre-existing and unrelated to this subtask's change**:
`git stash` (reverting both edited files back to HEAD) reproduces the exact same
collection error, so it is an environment issue (stale generated protobuf stubs vs.
the installed `protobuf` runtime package) predating this run, not something
introduced or fixable within this subtask's scope (`wiring.py`/test files only --
regenerating protobuf stubs or pinning the `protobuf` package is outside the
impacted-modules list). `agents/ingestion/test_segment_wiring.py` has no such
dependency (mocks `hivemind_pb2`/`hivemind_pb2_grpc` entirely via `sys.modules`, per
its own module docstring) and ran cleanly.

Per the task instructions, this limitation is disclosed explicitly rather than
fabricating a pass. `/cdr:verify` should re-attempt the live smoke test in an
environment with a consistent protobuf gencode/runtime pairing, or flag this
environment gap as a separate follow-up if it recurs.
