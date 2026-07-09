# Plan: task-3.2.2

1. `engine/rpc/server.go`:
   - `Server` struct embedding `hivemindv1.UnimplementedHiveMindServer` (forward compat +
     free `codes.Unimplemented` for ProposeSplit), holding: `*catalog.Catalog`,
     `*catalog.ContentStore`, `*catalog.IDAllocator`, `*graph.CSRGraph` (nullable),
     `*btree.NodeStore` (nullable), `btreeRootNodeID uint64`.
   - `NewServer(...)` constructor, dependency-injected (no lifecycle ownership -- mirrors
     `catalog.OpenContentStore`'s "does not own cat/w lifecycle" convention).
   - `PutSegment`: file_id==0 -> allocate + `ContentStore.Create`; else -> `ContentStore.Append`
     + `Catalog.Get` for `new_version`. Map `catalog.ErrNotFound` -> `codes.NotFound`, other
     errors -> `codes.Internal`.
   - `GetFile`: `ContentStore.Read` + `Catalog.Get` for version. Same error mapping.
   - `ReadPartial`: `ContentStore.ReadPartial`, map `[]catalog.HeaderOffset` ->
     `[]*hivemindv1.HeaderOffset`. Same error mapping.
   - `GraphNeighbors`: validate depth/maxNodes/edge-type-filter at the boundary (mirrors
     `graph.GraphNeighbors`'s own validation -> `codes.InvalidArgument`), explicit
     protoEdgeTypeToGraph/graphEdgeTypeToProto name-based conversion (NOT numeric cast),
     `Neighbor.hop` left at 0 with a doc comment explaining the known gap (see
     impact-analysis.json).
   - `SearchCandidates`: `btree.PrefixScan(query as prefix)`, cap to `max_results` if >0,
     constant placeholder `score`, doc comment explaining prefix-vs-general-query and
     constant-score choices.
2. `engine/rpc/server_test.go`:
   - `TestRPCServerHandlers` (matching issue's exact required test name) with subtests per
     RPC, using a real fixture-populated store (catalog+content+wal+btree+graph, same
     composition as `engine/integration_test.go`), not mocks.
   - Explicit `TestEdgeTypeConversionRoundTrip` (or subtest) covering the proto<->graph
     EdgeType mapping for all 5 enum values, guarding the mismatch found in
     architecture-discovery.md.
   - Error-path subtests: NotFound for unknown fileID (PutSegment-append, GetFile,
     ReadPartial), InvalidArgument for out-of-range GraphNeighbors depth/maxNodes.
   - `-race` since a gRPC server handles concurrent requests against shared engine state
     (graph CSR, catalog stripes); at least one concurrent-handler subtest.
3. Run `gofmt -l`, `go vet ./...`, `go build ./...`, `go test ./engine/rpc/... -race -timeout 60s`.
4. One local commit (`feat:` type), no push.
5. handoff.json with pointers only.
