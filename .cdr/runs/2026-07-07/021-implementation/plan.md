# Plan — Subtask 2b.3.4

1. Add `EdgeType` enum (`EdgeTypeInvalid` zero-value sentinel, `EdgeSplitSibling`,
   `EdgeRedirect`) and `Edge{Source, Target uint64; Type EdgeType}` to
   `engine/graph/edge_append.go`, with fixed-width (17-byte) little-endian
   encode/decode helpers matching `engine/catalog/record.go`'s conventions.
2. Add `EdgeAppender` wrapping a `wal.Writer` (via `wal.OpenWriter`), exposing
   `AppendEdge(Edge) error` (fsync-before-return, rejects `EdgeTypeInvalid`)
   and `Close() error`.
3. Add package-level `ReadAll(dir string) ([]Edge, error)`: lists
   `wal-<N>.log` segment files in `dir` in ascending order, decodes every
   record via `wal.ReadSegment` + `decodeEdge`, returns edges in strict
   on-disk append order. Missing dir -> empty slice, not an error.
4. Add `edge_append_test.go`:
   - `TestMinimalEdgeAppend` (required by test spec): append several edges,
     `Close`, then `ReadAll` from a fresh call to confirm durability, exact
     ordering, and that `Source` fileID is retrievable.
   - `TestMinimalEdgeAppendReopenAcrossProcesses`: confirms resuming
     `OpenEdgeAppender` on an existing dir appends after prior edges rather
     than overwriting (exercises `wal.OpenWriter`'s resume behavior through
     this primitive).
   - `TestAppendEdgeRejectsInvalidType`: fail-closed on invalid `EdgeType`.
   - `TestReadAllOnMissingDir`: "no edges yet" is not an error.
5. Self-consistency: `go build ./...`, `go vet ./...`, `gofmt -l .` (from
   `engine/`), `go test ./graph/... -run TestMinimalEdgeAppend -count=1
   -timeout 5m`, full `go test ./... -count=1 -timeout 15m`, and defensively
   `go test ./wal/... -race -count=1 -timeout 10m` (since `engine/graph` now
   imports `engine/wal`, though `wal/`'s own source is untouched).
6. One local commit (no push). `engine/split/` is untouched.
7. Write `handoff.json` with pointers only.
