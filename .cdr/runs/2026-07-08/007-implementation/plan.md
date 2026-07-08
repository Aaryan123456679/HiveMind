# Plan — 3.1.3 compaction

1. `engine/graph/edge_append.go`: add `EdgeEntityCooccur`, `EdgeLLMAsserted` to
   the `EdgeType` iota block (after `EdgeRedirect`, preserving existing byte
   values), with `String()` cases and doc comments explaining these two are
   added here (ahead of 3.1.4) because 3.1.3's own test spec requires
   `ENTITY_COOCCUR`.
2. `engine/graph/edgelog.go`: add `EdgeLog.TruncateNode(sourceFileID uint64)
   error`: takes `l.mu.Lock()`, closes+deletes `l.writers[sourceFileID]` if
   present, removes the per-node directory's on-disk segment files (all
   `wal-*.log` files via `listWALSegments` + `os.Remove`, then attempt
   `os.Remove` on the now-empty node dir — non-fatal if it still exists/has
   other files), so a subsequent `AppendEdge` lazily reopens a fresh
   `wal.Writer` at segment 0.
3. `engine/graph/compact.go` (new):
   - `edgeLogNodeIDs(root string) ([]uint64, error)`: list `root`'s
     subdirectories, parse each as `uint64`.
   - `mergeEdges(existing, incoming []CSREdge) []CSREdge`: key by
     `(Target, Type)`; for `EdgeEntityCooccur` sum `Weight`, max
     `LastUpdated`; for all other types, last-write-wins by `LastUpdated`
     (ties: incoming/log order wins, since log order is itself
     chronological).
   - `Compact(graphPath string, log *EdgeLog) (*CSRGraph, error)`:
     a. `LoadCSR(graphPath)`; if `os.IsNotExist`, start from an empty
        adjacency map (not an error — first-ever compaction).
     b. Convert loaded `*CSRGraph` into `map[uint64][]CSREdge` (one entry
        per existing node, via `Neighbors`).
     c. `edgeLogNodeIDs(log.root)`; for each fileID, `log.ReadNode(fileID)`,
        merge into that node's adjacency slice via `mergeEdges`.
     d. `BuildCSR(merged)`, `WriteCSR(graphPath, newGraph)` (atomic,
        unchanged from 3.1.1).
     e. AFTER `WriteCSR` returns nil: for each fileID actually read in step
        c, `log.TruncateNode(fileID)`; collect (not abort-on) truncate
        errors via `errors.Join`, since the new graph.dat is already
        durably correct regardless of truncate outcome.
     f. Return the new `*CSRGraph` and any truncate-phase errors (non-nil
        error here means "graph.dat updated successfully, but some edge
        logs may still hold already-compacted entries — safe to retry").
4. `engine/graph/compact_test.go` (new):
   - `TestCompaction_EntityCooccurWeightsSum`: append the same
     `(source,target,ENTITY_COOCCUR)` edge N times with `Weight:1`, compact,
     assert resulting CSR entry has `Weight == N`.
   - `TestCompaction_NonCooccurDedupLastWrite`: append the same
     `(source,target,SPLIT_SIBLING)` edge twice with different
     `LastUpdated`, compact, assert exactly one CSR entry with the later
     `LastUpdated`, not summed.
   - `TestCompaction_MergesWithExistingGraph`: write an initial graph.dat via
     `WriteCSR`, append additional log edges (including one more
     `ENTITY_COOCCUR` occurrence for an edge already in graph.dat), compact,
     assert weight continues accumulating from the pre-existing value.
   - `TestCompaction_NoEdgeLogIsNoop`: compact with an empty edge log root,
     assert existing graph.dat unchanged (byte-identical).
   - `TestCompaction_TruncatesLogsAfterCompaction`: after `Compact`,
     `ReadNode` on a compacted fileID returns nil/empty.
   - `TestCompaction_CrashBeforeRenameLeavesOldGraphAndLogsIntact` (crash
     injection, matching `engine/wal`'s crash-injection precedent): simulate
     a crash by making `WriteCSR`'s target directory read-only /
     interrupting before rename (e.g. via a temp-dir permission trick or by
     directly testing `mergeEdges`+`BuildCSR` succeed but a forced
     `WriteCSR` failure path leaves old file + full logs), assert old
     graph.dat (or absence) untouched and edge logs still fully intact
     (nothing truncated) — proving retry-safety.
   -`TestCompaction_TruncateFailureDoesNotLoseGraphUpdate`: force a
     `TruncateNode` failure for one node (e.g. lock/permission on that node
     dir) after a successful `WriteCSR`, assert `Compact` returns an error
     but the new `graph.dat` (reloaded via `LoadCSR`) already reflects the
     merged edges — proving the graph update is durable independent of
     truncate outcome.
5. `docs/LLD/graph.md`: add a "Compaction (subtask 3.1.3, `compact.go`)"
   subsection documenting weight-aggregation + crash-safety ordering
   decisions (mirroring existing 3.1.1/3.1.2 subsections' style).
6. `gofmt -l`, `go vet ./...`, `go build ./...`, `go test ./engine/graph/... -timeout 60s` (add `-race` if any new test spawns goroutines; compaction tests here are single-goroutine, so `-race` is best-effort/optional per file but will still be run repo-wide for safety).
7. One commit.
