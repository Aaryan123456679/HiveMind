# Plan — subtask 3.1.2

1. `engine/graph/edgelog.go`
   - Package doc comment explaining relationship to `edge_append.go` (distinct mechanism)
     and to `csr.go` (log is compaction input, CSREdge reused as entry shape).
   - `EdgeLog` struct: `root string`, `mu sync.RWMutex`, `writers map[uint64]*wal.Writer`.
   - `OpenEdgeLog(root string) (*EdgeLog, error)`: MkdirAll(root), return empty EdgeLog.
     Lazily opens per-node wal.Writer on first append/read for that fileID (so opening the
     manager itself never has to enumerate every existing per-node subdirectory).
   - `nodeDir(fileID uint64) string`: `filepath.Join(root, strconv.FormatUint(fileID, 10))`.
   - `getOrOpenWriter(fileID uint64) (*wal.Writer, error)`: double-checked-lock (RLock fast
     path, Lock+recheck slow path) over `writers` map; MkdirAll node dir; wal.OpenWriter
     with `defaultMaxSegmentBytes` (reuse const from edge_append.go, same package).
   - `AppendEdge(sourceFileID uint64, edge CSREdge) error`: reject `edge.Type ==
     EdgeTypeInvalid`; encode via CSREdge.encode (reuse csr.go's method, already exported
     within package); Append to that fileID's writer; fsync guaranteed by wal.Writer.Append.
   - `ReadNode(sourceFileID uint64) ([]CSREdge, error)`: list wal-*.log segments under that
     node's dir (reuse listEdgeSegments-style logic - extract a shared helper or duplicate
     minimal logic; decide during implementation whether to factor out a shared segment-
     listing helper used by both edge_append.go and edgelog.go without changing
     edge_append.go's public behavior).
   - `Close() error`: close all open writers, collecting errors via errors.Join.

2. `engine/graph/edgelog_test.go`
   - `TestPerNodeEdgeLogBasic`: open, append a few edges to 2-3 distinct fileIDs, ReadNode
     each, assert exact per-node contents, no cross-contamination.
   - `TestPerNodeEdgeLogInvalidType`: AppendEdge with EdgeTypeInvalid returns error, no
     record written.
   - `TestPerNodeEdgeLogMultiSegment`: force segment rotation (use a tiny maxSegmentBytes
     for one node, if the constructor allows overriding it - else rely on default and skip;
     decide during implementation) OR simply verify ordering across many appends within
     default segment size, deferring true rotation testing to reuse of existing wal-level
     rotation tests (already covered by wal package's own tests).
   - `TestPerNodeEdgeLog` (the required -race test): spawn many goroutines (e.g. 50) each
     targeting a distinct fileID, each appending N edges (e.g. 20) with small variation;
     use `sync.WaitGroup`; run with `-race`; after all goroutines finish, ReadNode every
     fileID and assert: (a) correct count, (b) correct content/order per node, (c) no edge
     leaked into a different node's log. Additionally include a lock-independence check:
     record wall-clock time for M concurrent appenders vs. a serial baseline (single
     appender doing M*K appends) and assert concurrent time is not close to M times the
     serial-single-append time, as a soft (not exact) proxy for "no cross-blocking" -
     document as best-effort/approximate in the test's comment given timing-based
     assertions are inherently loose.
   - `TestPerNodeEdgeLogClose`: Close is idempotent-safe to call once cleanly and reports
     errors from Close if any underlying wal.Writer.Close fails (best-effort check).

3. `docs/LLD/graph.md`: add a short subsection under 3.1.2's per-node edge log bullet
   pointing at `engine/graph/edgelog.go`, describing directory layout
   (`<root>/<fileID>/wal-N.log`), reused CSREdge entry shape, and non-blocking design (one
   wal.Writer per fileID + RWMutex double-checked-lock manager).

4. Run `gofmt -l`, `go vet ./...`, `go build ./...`, then
   `go test ./engine/graph/... -race -run TestPerNodeEdgeLog -timeout 60s` plus full
   package test run with `-timeout`.

5. Self-consistency check (build green + matrix coverage), then commit.
