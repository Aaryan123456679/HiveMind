# Self-consistency (internal sanity only -- NOT verification, per I4)

- `agents/.venv/bin/python -m pytest -q` (full `agents/` suite, the repo's correct pinned
  environment): **304 passed**, 0 failed (only pre-existing `DeprecationWarning`s from
  `grpc`'s SWIG bindings, unrelated to this change).
- Confirmed the system/anaconda `python`'s `hivemind_pb2`-import failures
  (protobuf gencode/runtime version mismatch) are pre-existing and reproduce identically on
  already-committed `agents/ingestion/test_shortlist.py`'s grpc tests -- not introduced or
  masked by this run.
- `git status --short agents/query api/` confirms `api/` is untouched; only
  `agents/query/pipeline.py`, `agents/query/test_pipeline.py`,
  `agents/query/test_query_e2e.py` modified, `agents/query/wiring.py` and
  `agents/query/test_wiring.py` added.
- Validation matrix (see `validation-matrix.md`): all 9 rows PASS.

This is internal build/test sanity only; independent verification is deferred to
`/cdr:verify` per invariant I4 (this agent does not verify its own work).
