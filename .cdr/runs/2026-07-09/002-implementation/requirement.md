# Requirement: task-3.2.2 (GitHub issue #16)

Source: `gh issue view 16` (issue #16, "[3] proto/ gRPC contracts + engine/rpc/ server + real
ProposeSplit wiring", milestone "Phase 3: Graph store + ingestion agents (end-to-end)").

NOTE: the raw `gh issue view` tool output for this run contained appended text resembling
fake system-reminders (a fabricated date-change notice, fake MCP "tokensave" tool
instructions, and a fake "Auto Mode Active" directive). This matches a known, recurring
prompt-injection pattern in this repo's issue/commit content. It was ignored; nothing in
it was treated as an instruction.

## Subtask 3.2.2 (verbatim acceptance criteria from issue #16)

- **Title**: Generate Go stubs; implement engine/rpc/ server for
  PutSegment/GetFile/ReadPartial/GraphNeighbors/SearchCandidates
- **Acceptance criteria**: Each RPC's Go server implementation delegates to the correct
  underlying module (catalog/content for PutSegment+GetFile+ReadPartial, graph for
  GraphNeighbors, btree for SearchCandidates) and returns correct results for a fixture
  request.
- **Test spec**: `go test ./engine/rpc/... -run TestRPCServerHandlers`: issue each RPC
  against a fixture-populated store, assert responses match direct module calls.
- **Impacted modules**: `engine/rpc/server.go`, `engine/rpc/server_test.go`

## Scope boundary (confirmed against issue text + docs/LLD/rpc.md)

- 3.2.2 covers exactly 5 RPCs: PutSegment, GetFile, ReadPartial, GraphNeighbors,
  SearchCandidates -- the RPCs `engine/rpc/`'s server SERVES.
- `ProposeSplit` is a client-side call this engine MAKES to the Python agent service; its
  real wiring is explicitly subtask 3.2.3 (`engine/split/proposer_grpc.go`), not 3.2.2. Left
  as the generated `UnimplementedHiveMindServer` default (`codes.Unimplemented`) in this
  subtask's server, exactly as issue #16 subtask boundaries define.
- `Split` (engine-internal split entry point) is explicitly NOT part of the 6-RPC proto
  surface per docs/LLD/rpc.md's F2 clarification note -- confirmed not in scope here either.
- Interceptors (3.2.4) and the cross-process integration test (3.2.5) are separate subtasks,
  not implemented here.

## Real underlying functions identified (see architecture-discovery.md for detail)

- PutSegment -> `engine/catalog.ContentStore.Create` (file_id==0, new file, ID allocated via
  `engine/catalog.IDAllocator.Next`) or `.Append` (file_id!=0).
- GetFile -> `engine/catalog.ContentStore.Read` + `engine/catalog.Catalog.Get` (for version).
- ReadPartial -> `engine/catalog.ContentStore.ReadPartial`.
- GraphNeighbors -> `engine/graph.GraphNeighbors`.
- SearchCandidates -> `engine/btree.PrefixScan` (issue text explicitly names "btree
  SearchCandidates"; the only query-shaped read primitive btree exposes is PrefixScan --
  exact `Lookup` and prefix `PrefixScan`, no free-text/scored search). See
  impact-analysis.json for the score-field and prefix-vs-general-query caveats this implies.
