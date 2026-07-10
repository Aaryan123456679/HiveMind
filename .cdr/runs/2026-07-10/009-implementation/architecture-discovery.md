# Architecture discovery — 3.4.4

## Real, already-implemented contracts (verified by reading source, not docs alone)

### `PutSegment` (`proto/hivemind.proto`, `engine/rpc/server.go:104-141`, `engine/rpc/server_test.go`)

```proto
message PutSegmentRequest  { uint64 file_id = 1; bytes content = 2; }
message PutSegmentResponse { uint64 file_id = 1; uint64 new_version = 2; }
```

- `file_id == 0` -> CREATE semantics: server allocates a new fileID (`idAlloc.Next()`),
  creates a `catalog.CatalogRecord{FileID, CurrentVersion:1, SizeBytes, Status:Active}`,
  writes content via `ContentStore.Create`.
- `file_id != 0` -> APPEND semantics: `ContentStore.Append(fileID, content)`, then
  re-reads the catalog record for the new `CurrentVersion`.
- **Gap found (pre-existing, out of scope for this subtask):** the CREATE path never
  sets `CatalogRecord.PathHash` from any request field, because `PutSegmentRequest`
  carries no path/string field at all — only `file_id` (uint64) + `content` (bytes).
  `CatalogRecord.PathHash` (`engine/catalog/record.go`) is the *only* path-shaped field
  in the entire catalog record; nothing in the current `PutSegment` server handler
  populates it, and no separate btree-insert call is wired from `PutSegment`. In other
  words: **the currently-shipped, already-verified `PutSegment` RPC has no way to
  register a newly created file's `new_topic_path` anywhere queryable** — `SearchCandidates`
  (which does a `btree.PrefixScan` over paths) would never surface a file created this
  way. This is a real, disclosed architecture gap in already-committed engine code
  (task-3.2.2), not something in `agents/ingestion/wiring.py`'s impacted-module scope
  to fix. Flagged forward (see handoff/pending.md) rather than silently worked around.
  Consequence for this subtask's design: `wiring.py` calls `PutSegment` exactly per its
  real contract (file_id + content bytes only) and does not invent a client-side
  workaround that pretends path registration works.

### Edge types (`proto/hivemind.proto`'s `EdgeType` enum, `docs/LLD/graph.md`)

`ENTITY_COOCCUR`, `LLM_ASSERTED`, `SPLIT_SIBLING`, `REDIRECT` — real enum values. But:

- **No write-path RPC for edges exists anywhere in the real system.** `proto/hivemind.proto`
  defines exactly 6 RPCs (`PutSegment`, `GetFile`, `ReadPartial`, `GraphNeighbors`,
  `SearchCandidates`, `ProposeSplit`) — `docs/LLD/rpc.md` explicitly states this list is
  frozen at 6 by task-3.2.1's acceptance criteria and any new RPC needs its own
  separately-scoped subtask. `GraphNeighbors` is read-only traversal. The only Go-side
  edge-write primitives (`engine/graph/edgelog.go`'s `EdgeLog.AppendEdge`,
  `engine/graph/edge_append.go`'s `EdgeAppender.AppendEdge`) are engine-internal Go
  types with **no gRPC handler wrapping them** — `grep -n "AddEdge\|CreateEdge" engine/rpc/*.go`
  returns nothing.
- Edge endpoints are `fileID`s only (`{ targetFileID, edgeType, weight, lastUpdated }`,
  per `docs/LLD/graph.md`'s "Edge shape" section) — there is no entity-as-graph-node
  concept anywhere in `engine/graph/`. This directly contradicts a literal reading of
  the issue's parenthetical suggestion ("edge weights between co-occurring entities
  (pairwise, within the same segment)") — entity-to-entity edges are not representable
  in the current graph model at all, since only fileIDs are valid edge endpoints.

### `entity.idx` (`docs/LLD/ingestion-agent.md` line 56, `docs/LLD/graph.md` line 105)

- Searched `engine/` and `agents/` for `entity.idx`, `EntityIndex`, `entity_idx`: **zero
  real matches** (the only hit is an unrelated Pygments lexer symbol name in a vendored
  dependency). `entity.idx` is prose-only in the LLD — no Go type, no file format, no RPC.
- `docs/LLD/graph.md` line 105 is the authoritative semantic description: "`ENTITY_COOCCUR`
  — incremented when the ingestion segmentation agent extracts co-occurring entities
  **across files**." Combined with "edge endpoints are fileIDs only," the correct reading
  is: `entity.idx` is a (not-yet-built) inverted index from entity name -> set of fileIDs
  that mention it, used so that when segment N mentions entity E, the wiring layer can
  look up which *other* files already mention E and create/increment an `ENTITY_COOCCUR`
  edge *between those files* (not between entity strings). This is the semantics
  implemented here (see plan.md), diverging from the task-instruction's more literal
  "pairwise between entities" suggestion — disclosed and justified by the graph model's
  actual constraints (fileID-only edge endpoints) and by the LLD's own "across files"
  wording, which is the more authoritative/specific source.

## Established Python-side client pattern (3.4.2's `shortlist.py`)

`GrpcSearchCandidatesClient`: lazy-imports `hivemind_pb2`/`hivemind_pb2_grpc` inside
`__init__`/`__call__` (never at module import time), with a `sys.path` fallback inserting
`agents/`'s absolute dir if the plain top-level import fails (since the generated stubs are
flat modules, not part of the `ingestion`/`llm` packages). Wraps a caller-supplied
`grpc.Channel`. This subtask's `GrpcPutSegmentClient` follows the exact same shape, since
`PutSegment` is a real RPC with the same stub-import characteristics.

## What has NO real RPC to wrap (disclosed, by design)

Because no edge-write RPC and no entity-index RPC exist in the real wire contract,
`wiring.py` defines a `SegmentWiringClient` Protocol whose `put_segment` method is backed
by a real `GrpcPutSegmentClient` (wrapping the real `PutSegment` RPC), but whose
`lookup_entity_files` / `index_entity` / `put_edge` methods have **no concrete
gRPC-backed implementation shipped in this commit** — they are Protocol members only,
satisfied by test doubles in `test_segment_wiring.py`, exactly mirroring the issue's own
test spec ("engine RPC client mocked"). This is not a shortcut: there is currently nothing
real to wrap. Closing this gap (adding real RPCs for entity-index lookups and graph edge
writes) is necessarily a *separate*, proto-touching subtask, out of this subtask's
`agents/ingestion/wiring.py`-only impacted-module scope. Flagged forward explicitly.

## LLD docs read

- `docs/LLD/ingestion-agent.md` ("What the Go engine does with each segment" section,
  lines 53-58) — matches the issue's wording almost verbatim, confirming this is the
  intended design target, not a misreading.
- `docs/LLD/rpc.md` — confirms 6-RPC freeze, `PutSegment` request/response field names.
- `docs/LLD/graph.md` — edge shape, `ENTITY_COOCCUR`/`LLM_ASSERTED` semantics, per-node
  edge log design (`engine/graph/edgelog.go`), confirms fileID-only edge endpoints.
