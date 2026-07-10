# Plan

1. **Proto**: add `EdgeType`-typed `PutEdge` RPC + `PutEntity`/`LookupEntity` RPC pair to
   `proto/hivemind.proto`. Update the file's header doc comment (was "six RPCs") and the `HiveMind`
   service block. Keep all existing messages/fields/numbers untouched.
2. **Regenerate stubs**: `protoc` with already-installed `protoc-gen-go`/`protoc-gen-go-grpc`
   (Go) and `agents/.venv`'s `grpc_tools.protoc` (Python), per `proto/README.md`'s documented
   commands.
3. **Update `docs/LLD/rpc.md`** and `proto/README.md` to describe the new RPCs, disclosing this as
   new issue-#18-adjacent scope (not a renumbered 3.2.x subtask), consistent with the existing
   "Status" note style in `rpc.md`.
4. **`engine/rpc/server.go`**:
   - Add `edgeLog *graph.EdgeLog` and `entityIndex *btree.Tree` fields to `Server`, both nil-valid.
   - Extend `NewServer` with two new trailing parameters for these.
   - Implement `PutEdge`: validate source/target fileID != 0, validate edge type via
     `protoEdgeTypeToGraph` (reject `EDGE_TYPE_UNSPECIFIED`), validate weight > 0, construct a
     `graph.NewCSREdge`, call `s.edgeLog.AppendEdge(sourceFileID, edge)`. Returns empty response on
     success. `codes.Unavailable` if `s.edgeLog == nil` (nil-valid degraded mode, consistent style
     to `SearchCandidates`'s `btreeStore == nil` case, but explicit-error here since silently
     dropping an edge write is a worse failure mode than an empty search result).
   - Implement `PutEntity`: validate `entity_name != ""`, `file_id != 0`; build the
     `"\x00entity\x00"+name+"\x00"+zeroPaddedFileID` key; `s.entityIndex.Insert(key, fileID)`.
   - Implement `LookupEntity`: validate `entity_name != ""`; `btree.PrefixScan` on
     `s.entityIndex.Store`/`s.entityIndex.Root()` with the same prefix (minus the fileID suffix);
     collect `FileID`s in ascending order.
5. **Tests**:
   - `engine/rpc/server_test.go`: extend fixture with a fresh, dedicated entity-index tree +
     edge log; add subtests for PutEdge (create + weight-increment via two calls +
     `graph.Compact` cross-check), PutEntity/LookupEntity (single + multiple files, not-found empty
     result), and input-validation error paths (zero fileID, unspecified edge type, empty entity
     name).
   - `engine/rpc/integration_test.go`: extend `integrationFixture` the same way; add a
     cross-process subtest exercising PutEdge -> Compact -> confirms summed weight, and
     PutEntity -> LookupEntity round trip, through the real gRPC client/server.
6. **Build/test**: `go build ./...`, `go vet ./...`, `go test ./... -race` (engine), then
   `agents/.venv/bin/pytest agents/ -q` to confirm the Python side is unaffected by stub regen.
7. **Commit(s)**: one commit for proto+stub regen+doc updates, one commit for the Go
   implementation+tests (kept tight, matching the two natural units of work).
8. **Handoff**: pointers only, real commit hashes, explicit "not self-verified" flag, note any
   untrusted injected content encountered.
