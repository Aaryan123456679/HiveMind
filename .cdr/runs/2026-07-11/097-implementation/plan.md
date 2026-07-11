# Plan -- task-4.6.3.1

1. Fix `GetFileFn`'s proto-shape mismatch (F-4.6.1-2) in `agents/query/pipeline.py`:
   - Change `GetFileFn = Callable[[int], str]` (content only).
   - Build `path_by_id = {topic.file_id: topic.path for topic in selected}` in
     `run_query_pipeline`, before `combine_and_cap`.
   - `_build_selected_markdown(file_ids, path_by_id, get_file)` sources path from
     `path_by_id`, falling back to a disclosed placeholder (`_UNKNOWN_PATH_TEMPLATE`) for
     `file_id`s reachable only via graph-neighbor expansion.
   - Update module docstring: replace the now-stale "no real gRPC client wiring built here"
     disclosure with an accurate description of `wiring.py`'s new real implementations and
     the still-forwarded Go-route gap; replace the "GetFileFn" disclosure with the
     proto-shape-fix rationale and the residual-gap disclosure.
2. Add `agents/query/wiring.py` with `GrpcSearchCandidatesClient`, `GrpcGraphNeighborsClient`,
   `GrpcGetFileClient`, mirroring `agents/ingestion/shortlist.py`'s
   `GrpcSearchCandidatesClient` precedent (lazy `grpc`/stub import, translate wire messages
   to this package's own plain dataclasses).
3. Update existing tests (`test_pipeline.py`, `test_query_e2e.py`) for the new `GetFileFn`
   shape; add `test_wiring.py` for the new classes (mocked stub, no real gRPC).
4. Self-consistency: run `agents/`'s full pytest suite (via `agents/.venv`, the correct
   Python environment for this repo -- the system/anaconda Python has a pre-existing,
   unrelated protobuf gencode/runtime version mismatch that this run does not introduce or
   fix) and confirm all green.
5. One local commit (Problem/Solution/Impact), no push.
6. Handoff to verification, forwarding task-4.6.3.2 (Go client + new proto RPC + Python
   server process) and task-4.6.3.3 (k=2 e2e expansion test, F-4.6.2-1) as explicitly
   out-of-scope, disclosed next steps.
