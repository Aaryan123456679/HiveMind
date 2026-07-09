# Architecture discovery: task-3.2.2

## Docs read (in order)

- `.cdr/index/*.jsonl` (task/file/feature/decision/regression indexes) -- no prior
  `engine/rpc/server.go` entries; task-3.2.1 (proto) is the only prior `engine/rpc/` work.
- `docs/LLD/rpc.md` -- confirms 5 served RPCs + 1 consumed RPC (ProposeSplit) + explicit F2
  note excluding `Split` from the proto surface (see requirement.md).
- `docs/HLD.md` cross-reference (via rpc.md) -- engine/rpc/ is the gRPC boundary between
  `api/` (HTTP gateway, primary client) and the storage core (`catalog/`, `graph/`, `mvcc/`).
- `proto/hivemind.proto` (source of truth for wire shapes, task-3.2.1, already committed).
- `engine/rpc/gen/hivemind.pb.go` / `hivemind_grpc.pb.go` (generated stubs) -- exact
  `HiveMindServer` interface, message getters, `EdgeType` enum values.

## Real entrypoints found (direct source reads, package by package)

### `engine/catalog` (content.go, catalog.go, record.go, idalloc.go)

- `ContentStore.Create(rec CatalogRecord, data []byte) (int64, error)` -- create path.
  Requires a fully-formed `CatalogRecord` (FileID, PathHash, CurrentVersion, SizeBytes,
  Status). FileID must be pre-allocated via `IDAllocator.Next()` before calling Create
  (mirrors `engine/integration_test.go`'s `TestStorageCoreIntegration` wiring pattern
  exactly -- catalog.Open -> NewCatalog -> NewIDAllocator -> wal.OpenWriter ->
  OpenContentStore, all sharing one root).
- `ContentStore.Append(fileID uint64, data []byte) (bool, error)` -- append path. Resolves
  fileID through `cat.Get` internally; returns wrapped `catalog.ErrNotFound` if fileID
  doesn't exist. Returns a threshold-crossed bool (split-trigger signal), not used by
  PutSegmentResponse's schema (proto only carries file_id/new_version) -- not surfaced.
- `ContentStore.Read(fileID uint64) ([]byte, error)` -- GetFile's content. Wrapped
  `ErrNotFound` on missing fileID.
- `ContentStore.ReadPartial(fileID uint64) ([]HeaderOffset, error)` -- ReadPartial's
  backing call, field-for-field matches proto's `HeaderOffset{header, offset}`. Wrapped
  `ErrNotFound` on missing fileID.
- `Catalog.Get(fileID uint64) (CatalogRecord, error)` -- used to fetch `CurrentVersion` for
  GetFile's response and PutSegment/Append's response (Append does not itself return the
  post-write CatalogRecord).
- **Important**: `PutSegmentRequest` (proto) carries only `file_id` + `content` -- NO path
  field. `CatalogRecord.PathHash` therefore cannot be populated meaningfully by this RPC;
  left as the zero value on Create. This is a proto-level design choice from task-3.2.1
  (already committed), not something 3.2.2 can or should change.

### `engine/graph` (traverse.go, edge.go, edge_append.go, csr.go)

- `GraphNeighbors(g *CSRGraph, fileID uint64, depth int, edgeTypeFilter EdgeType, maxNodes int) ([]CSREdge, error)`
  -- exact backing call. Validates depth in [0,2] and maxNodes>=0 itself (returns a plain
  `error`, no error type/code to switch on -- mapped to `codes.InvalidArgument` at the RPC
  boundary since these are caller-input validation failures, not internal faults).
- `g == nil` is treated by GraphNeighbors as "empty graph" (returns `nil, nil`), so the
  server can safely hold a nil `*graph.CSRGraph` if none is wired in yet.
- **EdgeType numeric mismatch (critical, found by direct comparison)**: internal
  `graph.EdgeType` iota order is `EdgeTypeInvalid=0, EdgeSplitSibling=1, EdgeRedirect=2,
  EdgeEntityCooccur=3, EdgeLLMAsserted=4` (`engine/graph/edge_append.go:45-79`). Proto's
  `hivemindv1.EdgeType` order is `EDGE_TYPE_UNSPECIFIED=0, ENTITY_COOCCUR=1, LLM_ASSERTED=2,
  SPLIT_SIBLING=3, REDIRECT=4` (`proto/hivemind.proto:31-37`). These do NOT line up
  numerically -- a naive `graph.EdgeType(int(protoEdgeType))` cast would silently produce
  wrong edge-type filtering/results. server.go must convert by explicit name-based mapping,
  not numeric cast.
- **Hop distance is lost**: `graph.GraphNeighbors`'s internal BFS tracks a per-candidate hop
  distance, but the function's public return type is `[]CSREdge` (Target, Type, Weight,
  LastUpdated only) -- hop is not part of `CSREdge` and is not returned. Proto's `Neighbor`
  message has a `hop` field the issue's acceptance criteria implicitly expects filled. Since
  "no new business logic" (do not reimplement BFS/hop-tracking in the RPC handler) is a hard
  constraint of this subtask, `Neighbor.hop` cannot be faithfully populated from the current
  `graph.GraphNeighbors` signature. Documented as a known limitation (hop always 0 in this
  implementation) rather than fabricated -- see impact-analysis.json.

### `engine/btree` (scan.go, lookup.go, insert.go)

- `PrefixScan(store *NodeStore, rootNodeID uint64, prefix string) ([]ScanEntry, error)` --
  the only query-shaped read primitive; `ScanEntry{Path, FileID}`. No relevance score is
  computed by btree at all. Issue text itself names "btree SearchCandidates" as the intended
  delegation target, confirming PrefixScan (there is no other btree read primitive:
  `Lookup` is exact-match only) is the intended backing call, using `query` as a literal
  string prefix (not general substring/fuzzy search).
- `CandidateTopic.score` (proto) has no backing computation in btree; a constant placeholder
  score is used (documented in code) since inventing a real relevance-ranking algorithm here
  would be new business logic outside this subtask's "thin adapter" scope.
- Nothing in the 5 in-scope RPCs inserts into the B+Tree (PutSegment's proto shape has no
  path field -- see catalog section above), so the server's btree root is a constructor
  parameter/injected dependency, not something PutSegment maintains. Test fixtures populate
  it directly via `btree.Insert`, mirroring `engine/integration_test.go`'s wiring.

## Wiring pattern (reused, not invented)

`engine/integration_test.go`'s `TestStorageCoreIntegration` is the existing, real example of
composing `catalog.Open` -> `catalog.NewCatalog` -> `catalog.NewIDAllocator` ->
`wal.OpenWriter` -> `catalog.OpenContentStore` -> `btree.OpenIndexFile` ->
`btree.NewNodeStore` -> `btree.NewNodeAllocator`, all sharing one root dir. `server_test.go`
reuses this exact composition for its fixture setup (per the issue's test spec: "issue RPC
against fixture-populated store").
