# Requirement — issue #43, commit 3/3

## Source
`gh issue view 43 --json body -q '.body'` (verbatim, re-pulled for this run — not
paraphrased from prior session state):

> [4.5] engine/rpc+catalog: PutSegment CREATE never sets PathHash (F4, needs
> proto/wire-contract change)
>
> **Problem**: `engine/rpc/server.go`'s `PutSegment` handler's CREATE path never
> sets `catalog.CatalogRecord.PathHash`. This means files created via
> `PutSegment` (i.e. every new topic created by the ingestion/segmentation
> pipeline) are **not discoverable via `SearchCandidates`** afterward.
>
> **Root cause**: `PutSegmentRequest` in `proto/hivemind.proto` had **no path
> field at all** — it was `{file_id, content}`. The caller
> (`agents/ingestion/wiring.py`'s `execute_segment`) genuinely knows the topic
> path (`target_topic`/`new_topic_path`) at call time but there was no wire
> slot to carry it to the server.
>
> **Required fix**:
> 1. Add a path field to `PutSegmentRequest` in `proto/hivemind.proto`.
> 2. Regenerate both Go and Python stubs.
> 3. Update `engine/rpc/server.go`'s `PutSegment` CREATE handler to compute
>    and set `PathHash` on the new field.
> 4. Update all callers (`agents/ingestion/wiring.py`'s
>    `GrpcPutSegmentClient`, any Go-side callers) to populate the new field.
> 5. Add a regression test proving a newly-`PutSegment`-created file IS
>    discoverable via `SearchCandidates` immediately afterward.

## Scope of this run (3rd and final commit)

Commits already landed and verified this session:
- `107f982` (1/3): added `string path = 3;` to `PutSegmentRequest` in
  `proto/hivemind.proto`; regenerated Go (`engine/rpc/gen/hivemind.pb.go`) and
  Python (`agents/hivemind_pb2.py`/`.pyi`) stubs. Purely additive/wire-compatible;
  no caller populated it yet.
- `14902e8` (2/3): `engine/rpc/server.go`'s `PutSegment` CREATE handler now
  computes `PathHash` via new `catalog.HashPath` and calls
  `pathIndex.Insert(path, fileID)` into the same B+Tree `SearchCandidates`
  reads from. Added Go-side end-to-end regression test
  (`TestRPCIntegration/PutSegment_Create_DiscoverableViaSearchCandidates`)
  proving discoverability — but that test drives the server directly via a Go
  RPC client, not via the real Python ingestion path.

**Remaining scope (this commit, 3/3)**: `agents/ingestion/wiring.py`'s
`GrpcPutSegmentClient.put_segment` still builds
`PutSegmentRequest(file_id=file_id, content=content)` — it does not populate
the new `path` field at all. Because the field defaults to `""` on the wire,
every `PutSegment` call issued by the real Python ingestion pipeline currently
sends an empty path, so the server-side `PathHash`/`pathIndex.Insert` wiring
landed in commit 2/3 is never actually exercised by real ingestion traffic —
only by Go-side tests. This commit closes that gap: thread the real topic
path (`SegmentResult.new_topic_path` for `CREATE_NEW`,
`SegmentResult.target_topic` for `APPEND_EXISTING`, matching the proto's own
"used only when file_id == 0 ... ignored on append" comment) from
`execute_segment` through the `SegmentWiringClient` Protocol into
`GrpcPutSegmentClient.put_segment`'s `PutSegmentRequest.path`.

## Acceptance criteria for this commit
- `GrpcPutSegmentClient.put_segment` accepts a `path: str` argument and passes
  it through to `PutSegmentRequest(..., path=path)`.
- `SegmentWiringClient` Protocol, `GrpcSegmentWiringClient`, and
  `execute_segment`'s call site are updated consistently so the real path
  flows end-to-end from `SegmentResult` to the wire request.
- Existing fakes/tests exercising the `SegmentWiringClient` Protocol
  (`test_segment_wiring.py`, `test_segment_fixtures.py`) are updated to match
  the new signature, not just left broken.
- A new test (mock/fake gRPC stub) asserts `PutSegmentRequest` is constructed
  with `path=` set to the segment's actual source path (both `CREATE_NEW` via
  `new_topic_path` and `APPEND_EXISTING` via `target_topic`).
- No change to `proto/hivemind.proto`, generated stubs, or any Go file in this
  commit — those were already landed in commits 1/3 and 2/3.

## Explicitly out of scope for this run
- Any further engine/Go changes.
- Any change to `docs/LLD/ingestion-agent.md`'s narrative beyond what's
  already accurate (the module docstring's "new_topic_path cannot be
  registered anywhere queryable today" disclosure in `wiring.py` becomes
  stale once this commit lands and should be corrected, but that is a
  docstring/comment cleanup within the same file, not a new feature).
