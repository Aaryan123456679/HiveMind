# Plan — Subtask 3.1.4

1. Create `engine/graph/edge.go`:
   - `ValidEdgeType(t EdgeType) bool` — true iff t is one of the 4 defined constants
     (`EdgeSplitSibling`, `EdgeRedirect`, `EdgeEntityCooccur`, `EdgeLLMAsserted`).
   - `EdgeTypeName(t EdgeType) (string, error)` — canonical LLD tokens
     (`"SPLIT_SIBLING"`, `"REDIRECT"`, `"ENTITY_COOCCUR"`, `"LLM_ASSERTED"`); error for
     any invalid type (distinct from `EdgeType.String()`'s debug-format fallback which
     never errors, used in error messages elsewhere).
   - `ParseEdgeType(name string) (EdgeType, error)` — inverse of `EdgeTypeName`, exact
     case-sensitive match against the 4 canonical tokens; error otherwise. Exists ahead
     of 3.1.5's `edgeTypeFilter` parameter, which will need this.
   - `NewCSREdge(target uint64, t EdgeType, weight uint32, lastUpdated int64) (CSREdge, error)`
     — validated constructor, rejects an undefined type via `ValidEdgeType`.
2. `engine/graph/csr.go`:
   - `decodeCSREdge(data []byte) (CSREdge, error)` — validates `Type` via `ValidEdgeType`
     after decode, returns descriptive error including the raw byte value.
   - `LoadCSR`: propagate `decodeCSREdge`'s error (wrapped with file path + index
     context).
   - `WriteCSR`: before encoding, validate every edge in `g.edges` via `ValidEdgeType`;
     return an error naming the offending source node / index if any edge has an
     undefined type, so a bug upstream (e.g. a future compaction change) is caught before
     ever touching disk rather than being silently persisted.
3. `engine/graph/edgelog.go`:
   - `EdgeLog.AppendEdge`: replace `edge.Type == EdgeTypeInvalid` check with
     `!ValidEdgeType(edge.Type)`, updating the doc comment to remove the "3.1.4's job"
     deferral note (now implemented) and describe the new behavior.
   - `ReadNodeAfter`: update the `decodeCSREdge` call site for the new `(CSREdge, error)`
     signature, wrapping/propagating any decode error with segment path context (mirrors
     the existing malformed-record-length error already in this function).
4. `engine/graph/edge_test.go` (new):
   - `TestEdgeTypes/ValidEdgeType`: table test over all 4 valid constants (true),
     `EdgeTypeInvalid` (false), and several undefined byte values (5, 200, 255) (false).
   - `TestEdgeTypes/NameParseRoundTrip`: `EdgeTypeName` then `ParseEdgeType` round-trips
     to the original value for all 4 types; `ParseEdgeType` on garbage string errors;
     `EdgeTypeName` on invalid type errors.
   - `TestEdgeTypes/NewCSREdge`: valid type succeeds and fields match input; invalid type
     (including `EdgeTypeInvalid` and an undefined byte) errors and returns zero value.
   - `TestEdgeTypes/CSREdgeEncodeDecodeRoundTrip` (via package-internal access, same
     package): encode/decode round-trips correctly for all 4 valid types; a manually
     corrupted encoded buffer with an undefined type byte errors on decode rather than
     silently succeeding (this is the regression-class check for the "silently
     corrupting bugs" concern raised in the task).
   - `TestEdgeTypes/EdgeLogRejectsUndefinedType`: `EdgeLog.AppendEdge` accepts all 4
     valid types (readable back via `ReadNode`) and rejects `EdgeTypeInvalid` plus an
     undefined byte value (e.g. 200) without writing anything durable (verify via
     `ReadNode` returning no new entries after a rejected append).
   - `TestEdgeTypes/WriteCSRRejectsUndefinedType`: `BuildCSR` + `WriteCSR` with an edge
     carrying an undefined type errors and does not create/overwrite the target file
     (verify file absence, or that a pre-existing file is left untouched).
5. Run `gofmt -l`, `go vet ./...`, `go build ./...`, and
   `go test ./engine/graph/... -run TestEdgeTypes -timeout 60s -v` plus the full package
   suite `go test ./engine/graph/... -race -timeout 120s` to confirm no regression in
   3.1.1-3.1.3's existing tests.
6. Self-consistency check, commit, handoff.
