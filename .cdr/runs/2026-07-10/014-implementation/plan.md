# Plan

1. Read `.cdr/commits/task-3.4.4-engine-edge-rpc.md` for exact RPC contracts.
2. Read `proto/hivemind.proto` (PutEdge/PutEntity/LookupEntity messages + EdgeType
   enum) and `agents/hivemind_pb2.pyi` to confirm stubs already regenerated.
3. Read `agents/ingestion/wiring.py` in full (via `sed`/`awk`, not the lossy `Read`
   tool output encountered mid-session -- see self-consistency notes) and
   `agents/ingestion/shortlist.py` for the `GrpcSearchCandidatesClient` pattern to
   mirror.
4. Read `agents/ingestion/test_segment_wiring.py` in full for the existing test
   spec/pattern (`_FakeWiringClient`, mocked-`sys.modules` gRPC client tests).
5. Implement `GrpcEntityEdgeClient` (lookup_entity_files/index_entity/put_edge over
   LookupEntity/PutEntity/PutEdge) and `GrpcSegmentWiringClient` (composes
   `GrpcPutSegmentClient` + `GrpcEntityEdgeClient`) in `wiring.py`. Update stale
   docstrings (module-level "Real vs. Protocol-only RPC surface" section,
   `SegmentWiringClient`'s docstring, `GrpcPutSegmentClient`'s docstring) to reflect
   the now-closed gap.
6. Extend `test_segment_wiring.py` with tests for the new classes, following the
   existing `test_grpc_put_segment_client_translates_request_response` mocked-
   `sys.modules` pattern exactly. Cover: request/response translation for each of
   the 3 methods, default `weight_delta`, RPC-error propagation (unwrapped, so
   `execute_segment` can catch/collect it), `GrpcSegmentWiringClient` delegation for
   all 4 methods, and one end-to-end `execute_segment` run against
   `GrpcSegmentWiringClient`.
7. Run `agents/.venv/bin/pytest agents/ingestion/test_segment_wiring.py -q`, then
   the full `agents/.venv/bin/pytest agents/ -q` suite, then
   `agents/.venv/bin/ruff check agents/ingestion/wiring.py
   agents/ingestion/test_segment_wiring.py`.
8. Write CDR artifacts (this plan + requirement + architecture-discovery +
   impact-analysis + validation-matrix + self-consistency + handoff).
9. One local commit (Problem:/Solution:/Impact:), no push, no GitHub state changes.
10. Stop -- do not self-verify (I4); hand off to `/cdr:verify`.
