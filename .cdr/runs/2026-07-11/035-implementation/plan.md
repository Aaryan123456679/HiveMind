# Plan — Subtask 4.5.11.1

1. Confirm (already done in architecture-discovery) that `engine/graph/compact.go`
   and `engine/graph/edgelog.go` at `HEAD` already implement the root-cause fix
   (never-reuse segment numbering via `wal.WriteSegmentFloor`, landed in commit
   `ed57468`), and that it is already covered by
   `TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost`.
2. Add a new test, `TestCompactNormalTruncateThenAppendThenCompactAgain`, to
   `engine/graph/compact_test.go`, placed immediately after
   `TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost` (adjacent,
   related regression tests). This test:
   - Uses the issue's own worked numbers exactly: `AppendEdge(weight=3)` ->
     `Compact` (must fully succeed, no failure injection) -> `AppendEdge(weight=5)`
     -> `Compact` again -> assert `LoadCSR` shows `Weight=8` for the
     `EdgeEntityCooccur` edge (source -> target), not `3`.
   - Additionally asserts the edge log for that node is not silently "empty but
     lossy" -- i.e. asserts the merged CSR reflects both appends, which is the
     issue's literal repro assertion ("edge log confirmed empty too, so the
     second edge exists nowhere" was the failure symptom on the buggy code).
   - Does NOT duplicate the third-round-trip / failure-injection assertions
     already covered by neighboring tests -- stays minimal and named exactly per
     the issue's test spec, so `go test ./engine/graph/... -run
     TestCompactNormalTruncateThenAppendThenCompactAgain` is a precise,
     self-contained acceptance command.
3. Do not modify `compact.go` or `edgelog.go` -- no production code change is
   needed or warranted; the fix is already correct.
4. Self-consistency: run
   - `go test ./engine/graph/... -race -v -run TestCompactNormalTruncateThenAppendThenCompactAgain`
   - `go test ./engine/graph/... -race -v` (full package)
   - `go test ./... -race` from the `engine` module (full engine suite)
   All must be green with zero regressions before committing.
5. One local commit (test-only diff), message documenting the exact data-loss
   scenario this pins down, noting the production fix landed in `ed57468` and
   this commit adds the acceptance-criteria-mandated regression test by its
   required name.
6. Write self-consistency.json, handoff.json (pointers only), leave
   verification of "is issue #49 subtask 4.5.11.1 now resolved" to
   `/cdr:verify` per invariant I4 -- this agent does not mark anything verified
   or update regression.jsonl's `resolved` field itself.
