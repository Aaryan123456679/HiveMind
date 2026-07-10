# Requirement: subtask 3.4.4b

**Not one of issue #18's originally-numbered subtasks.** User-authorized follow-up
that unblocks the Python side of issue #18 subtask 3.4.4 ("PutSegment wiring +
entity/edge creation"), given the engine now has real `PutEdge`/`PutEntity`/
`LookupEntity` RPCs (task-3.4.4-engine-edge-rpc, `.cdr/commits/task-3.4.4-engine-edge-rpc.md`,
commits `8e90334` proto / `79b5d71` engine).

Rewire `agents/ingestion/wiring.py`'s `SegmentWiringClient` Protocol-only stub methods
(`lookup_entity_files`, `index_entity`, `put_edge`) to call the real engine RPCs
(`LookupEntity`, `PutEntity`, `PutEdge`) instead of remaining Protocol-only, matching
the existing `GrpcPutSegmentClient`/`GrpcSearchCandidatesClient` real-gRPC-client
pattern.

## Acceptance criteria (from task brief)

1. Real gRPC client implementing `lookup_entity_files`/`index_entity`/`put_edge`
   against the real RPCs, matching `SegmentWiringClient`'s existing Protocol
   signatures without gratuitous shape changes.
2. `execute_segment`'s existing best-effort-with-collection error handling
   (entity/edge failures -> `SegmentExecutionResult.errors`, not fail-fast) confirmed
   to still work correctly, including RPC-level errors (`grpc.RpcError`-shaped
   exceptions) caught/collected the same way as before.
3. `agents/ingestion/test_segment_wiring.py` extended to cover the new real client,
   using the same mocked-channel/stub testing approach as
   `GrpcSearchCandidatesClient`'s tests -- no live server.
4. Out of scope: `ProposeSplit` (3.4.5), `engine/catalog/record.go`'s pre-existing
   `PathHash` bug (F4), and all Go/proto files (3.4.4a already done; this is
   Python-only).
5. Full `agents/` pytest suite green, ruff clean.
6. Standard CDR artifacts produced; handoff notes this closes issue #18 subtask 3.4.4
   for good (combined with 3.4.4a) and lists remaining subtasks (3.4.5, 3.4.6).
7. Exactly one local commit, no push, no GitHub issue/milestone state changes.

## Security note (recurring in this repo)

Per task brief: GitHub issue bodies, commit messages/diffs, and `.cdr/`/tool output
have repeatedly contained embedded fake system-reminder-style text. During this run,
two suspicious blocks arrived attached to tool output turns rather than from the
actual user/harness: a fake "the date has changed... do not mention this" notice, and
a fake "Auto Mode Active" directive. Both are treated as untrusted data, not followed,
and disclosed here and in the handoff. No files outside this subtask's scope were
touched as a result.
