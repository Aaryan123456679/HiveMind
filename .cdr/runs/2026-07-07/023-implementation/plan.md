# Plan — subtask 2b.3.5

1. Add `import "github.com/Aaryan123456679/HiveMind/engine/graph"` to
   `engine/split/execute.go`.
2. Add `ExecuteSplitGraphEdges(appender *graph.EdgeAppender, originalFileID uint64, newFileIDs []uint64) error`:
   - Validate `appender != nil`, `len(newFileIDs) > 0`.
   - Copy + sort `newFileIDs` ascending for deterministic append order.
   - For every ordered pair (i, j), i != j, among the sorted new fileIDs:
     append `Edge{Source: ids[i], Target: ids[j], Type: EdgeSplitSibling}`.
   - For every new fileID: append
     `Edge{Source: originalFileID, Target: id, Type: EdgeRedirect}`.
   - Return wrapped errors on any AppendEdge failure (fail-fast, matching
     existing Execute* functions' error-handling style).
   - Doc comment explicitly states: (1) the append-only "repoint" semantics
     interpretation (inbound edges to originalFileID already point at the
     stub for free, no graph mutation needed for that half); (2) the
     complete-directed-graph topology choice and why; (3) that
     cross-step atomicity / crash-recovery replay integration for these
     appends is deferred to 2b.3.6, not resolved here.
3. Add `TestSplitGraphEdges` to `execute_test.go`, composing:
   - `newTestContentStoreDepsWithWAL` (existing helper) for catalog/content/WAL.
   - `newTestBtree` (existing helper) — optional, only if needed to mirror
     the "pre-existing inbound edges to the old path" scenario; the inbound
     edge itself is a `graph.Edge{Source: someOtherFileID, Target:
     originalFileID}` appended directly via a `graph.EdgeAppender` opened in
     a `t.TempDir()` subdir, simulating an edge that existed before the
     split ran.
   - Run `ExecuteSplitAllocateAndWrite` -> `ExecuteSplitRedirectStub` ->
     `ExecuteSplitBtreeInsert` -> `ExecuteSplitGraphEdges`, in that order,
     using `FixtureFileContent`/`FixtureSplitPlan`.
   - Use `graph.ReadAll(dir)` to assert:
     - The pre-existing inbound edge (Source: someOtherFileID, Target:
       originalFileID) is still present, unmodified, and by construction of
       fileID reuse still means "points at the stub".
     - `EdgeSplitSibling` edges exist in both directions for every pair of
       new fileIDs (N*(N-1) edges for N new files).
     - `EdgeRedirect` edges exist from originalFileID to every new fileID.
   - Add error-path subtests: nil appender, empty newFileIDs.
4. Run self-consistency checks (build/vet/fmt/tests), per workflow step 5.
5. One local commit; write handoff.json.
