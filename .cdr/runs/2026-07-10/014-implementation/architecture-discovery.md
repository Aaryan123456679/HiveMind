# Architecture discovery

## Token order followed

index/ -> memory/handoffs -> targeted LLD/milestone doc -> touched files -> source.

- `.cdr/commits/task-3.4.4-engine-edge-rpc.md` (milestone record) read first: exact
  RPC contracts, key-encoding, design rationale, and explicit statement that 3.4.4b
  is the required follow-up.
- `proto/hivemind.proto`: confirmed `PutEdge(source_file_id, target_file_id,
  edge_type, weight)`, `PutEntity(entity_name, file_id)`,
  `LookupEntity(entity_name) -> file_ids[]` wire shapes, and `EdgeType` enum
  (`ENTITY_COOCCUR`/`LLM_ASSERTED`/`SPLIT_SIBLING`/`REDIRECT`, matching
  `wiring.py`'s own `ENTITY_COOCCUR`/`LLM_ASSERTED` string constants exactly).
- `agents/hivemind_pb2.pyi`: confirmed generated stubs already contain
  `PutEdgeRequest`/`PutEdgeResponse`/`PutEntityRequest`/`PutEntityResponse`/
  `LookupEntityRequest`/`LookupEntityResponse` message classes -- no regeneration
  needed, stubs were already current from the 8e90334/79b5d71 milestone.
- `agents/ingestion/wiring.py` (full read): `SegmentWiringClient` Protocol,
  `execute_segment`'s fail-fast (pre-`PutSegment`) vs. best-effort-with-collection
  (post-`PutSegment`) error handling, `GrpcPutSegmentClient`'s lazy-import +
  `sys.path` fallback pattern.
- `agents/ingestion/shortlist.py`: confirmed `GrpcSearchCandidatesClient`'s exact
  pattern (lazy `_import_hivemind_grpc_modules()` helper, plain translation
  methods, no `__all__`, CWD-independent via `sys.path` fallback) -- `wiring.py`
  already reuses the identical helper name/shape, so the new client follows suit
  without reinventing anything.
- `agents/ingestion/test_segment_wiring.py` (full read): confirmed test spec
  ("engine RPC client mocked entirely"), `_FakeWiringClient`'s call-recording
  design, and `test_grpc_put_segment_client_translates_request_response`'s
  mocked-`sys.modules` pattern (`monkeypatch.setitem` + `MagicMock` stand-ins for
  `hivemind_pb2`/`hivemind_pb2_grpc`) as the template for the new client's tests.

## Key design decisions

1. **No Protocol shape change.** `PutEdgeRequest.weight` is documented (proto
   comment) as "this call's own occurrence weight ... not a running total" --
   exactly what `SegmentWiringClient.put_edge`'s existing `weight_delta` parameter
   already means. `LookupEntityResponse.file_ids` is a plain `repeated uint64` --
   directly castable to `Sequence[int]`. `PutEntityResponse` is empty
   (`__slots__ = ()`) -- nothing to translate for `index_entity -> None`. So the
   Protocol's existing signatures needed zero changes.
2. **`edge_type: str -> EdgeType` translation** via `hivemind_pb2.EdgeType.Value(
   edge_type)`. Safe because the module's own `ENTITY_COOCCUR`/`LLM_ASSERTED`
   constants are defined (module docstring) to be exactly the enum's canonical
   wire names -- the only two values `execute_segment` ever passes.
3. **New classes, not a change to `GrpcPutSegmentClient`.** Added
   `GrpcEntityEdgeClient` (wraps `PutEdge`/`PutEntity`/`LookupEntity`) as a sibling
   to the unmodified-in-behavior `GrpcPutSegmentClient`, plus a composed
   `GrpcSegmentWiringClient` that delegates to one instance of each -- giving
   callers a single object fully satisfying `SegmentWiringClient` without forcing
   `GrpcPutSegmentClient` itself to grow unrelated responsibilities. Composition
   (not multiple inheritance) keeps each sub-client's lazy-import `__init__`
   unambiguous.
4. **Error handling: nothing to change in `execute_segment`.** It already catches
   bare `Exception` (with `# noqa: BLE001`, deliberately broad per its own
   docstring) around every `lookup_entity_files`/`put_edge`/`index_entity` call in
   the best-effort phase. `grpc.RpcError` is an `Exception` subclass, so it is
   already caught by the existing `except Exception` clauses -- confirmed by a new
   test (`test_grpc_entity_edge_client_rpc_error_propagates_unwrapped`) that the
   client itself does NOT swallow RPC errors (they must propagate out of the
   client for `execute_segment` to catch and collect them), plus the existing
   fake-client-based error-collection tests already covering `execute_segment`'s
   side of that contract unchanged.
