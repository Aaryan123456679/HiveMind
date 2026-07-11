# Architecture discovery — issue #43, commit 3/3

## Order followed
`.cdr/index/*` -> `docs/HLD.md` (skimmed, no ingestion/rpc wire-level detail
there) -> `docs/LLD/ingestion-agent.md` + `proto/README.md` (rpc/proto docs) ->
`git show 107f982` / `git show 14902e8` (the two already-landed commits' diffs,
per explicit run instruction, to see exact proto/signature shape) -> touched
source files (`agents/ingestion/wiring.py`,
`agents/ingestion/test_segment_wiring.py`,
`agents/ingestion/test_segment_fixtures.py`).

## Key facts established

1. **Proto shape (from `107f982`)**: `PutSegmentRequest` now has
   `string path = 3;`, documented as "used only when file_id == 0 (create
   semantics) ... Ignored on append (file_id != 0)". Python stub
   `agents/hivemind_pb2.py` regenerated already; `PutSegmentRequest` accepts a
   `path=` kwarg today (unused by any caller).

2. **Server wiring (from `14902e8`)**: `engine/rpc/server.go`'s CREATE handler
   now does `catalog.HashPath(path)` -> `PathHash`, and
   `pathIndex.Insert(path, fileID)` into the same `btree.Tree`
   `SearchCandidates` reads via `PrefixScan`. This only fires when the
   incoming `PutSegmentRequest.path` is non-empty (server-side `if path != ""`
   guard per that commit's server.go diff). Confirmed via Go-side
   integration test `TestRPCIntegration/PutSegment_Create_DiscoverableViaSearchCandidates`,
   which drives the server directly (not through Python).

3. **Current Python call chain** (`agents/ingestion/wiring.py`):
   - `SegmentWiringClient` (Protocol) declares
     `put_segment(self, file_id: int, content: bytes) -> PutSegmentResult`.
   - `execute_segment()` (the only in-repo caller) computes `file_id_arg`
     (0 for `CREATE_NEW`, resolved fileID for `APPEND_EXISTING`) and calls
     `rpc_client.put_segment(file_id_arg, content_markdown.encode("utf-8"))`
     — no path is passed anywhere today.
   - `GrpcPutSegmentClient.put_segment` builds
     `hivemind_pb2.PutSegmentRequest(file_id=file_id, content=content)` —
     `path` field omitted entirely, so it serializes as `""` on the wire.
     This is the exact gap this commit closes.
   - `GrpcSegmentWiringClient.put_segment` is a thin delegator to
     `GrpcPutSegmentClient.put_segment`.
   - `SegmentResult` (from `agents/ingestion/segment.py`) already carries
     `target_topic: str` (non-empty iff `topic_action == "APPEND_EXISTING"`)
     and `new_topic_path: str` (non-empty iff `topic_action == "CREATE_NEW"`)
     — the real path is available at `execute_segment`'s call site today,
     it's just not threaded through.

4. **Test fakes needing signature updates** (both implement
   `SegmentWiringClient` structurally, both record
   `put_segment_calls: list[tuple[int, bytes]]`):
   - `agents/ingestion/test_segment_wiring.py::_FakeWiringClient` (5 assertion
     sites on `put_segment_calls`).
   - `agents/ingestion/test_segment_fixtures.py::_FakeWiringClient` (shared
     fixture used by other ingestion tests; 1 assertion site).
   - `test_segment_wiring.py` also has direct tests of
     `GrpcPutSegmentClient.put_segment` and `GrpcSegmentWiringClient.put_segment`
     that call `client.put_segment(0, b"content...")` with 2 positional args —
     these need a 3rd `path` arg once the signature changes.

## Blast radius (confirmed via grep, no other in-repo callers)
`grep -rn "put_segment"` across `agents/` shows exactly 3 files reference the
method: `wiring.py` (definition sites), `test_segment_wiring.py`,
`test_segment_fixtures.py`. No other module/agent calls `put_segment` or
`SegmentWiringClient` directly. `docs/LLD/ingestion-agent.md` does not
document the `put_segment` Python signature at the parameter level (only the
RPC-level "Executes append/create via PutSegment" narrative), so no LLD edit
is required for a signature change confined to this file's internal Protocol.

## Decision: thread `path: str` as a required positional/keyword param
Chosen over alternatives:
- **Compute path only inside `GrpcPutSegmentClient`** — rejected: the real
  path is a property of the `SegmentResult`/`execute_segment` call site, not
  something the RPC client can derive on its own without breaking the
  existing "Protocol client only needs file_id+content" abstraction, and the
  Protocol-based fake clients need the same info for the discoverability
  property to hold for both real and fake implementations.
- **Optional `path: str = ""` default everywhere** — rejected: would leave
  `execute_segment` silently able to omit passing the real path (regressing
  right back to the bug this run exists to fix) if any future call site
  forgets it. Making it a required parameter on the Protocol forces every
  implementer (real and fake) to handle it explicitly, matching this issue's
  intent that this path actually be exercised end-to-end, not just possible.
