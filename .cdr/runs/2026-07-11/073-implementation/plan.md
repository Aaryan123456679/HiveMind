# Plan — subtask 4.5.3.6

1. Read `engine/split/split_race_test.go`'s `TestReaderDuringSplit` in full; confirm root
   cause is the fixed `readerIterations=400` + `time.Sleep(10us)` sleep-based overlap
   assumption underlying the `sawPostSplit == 0` false-failure mode.
2. Read `engine/split/execute.go`'s existing `atomicCommitHook` seam (already present,
   pre-dating this subtask, used by `TestSplitAtomicCommit`'s crash-injection subtests) to
   confirm a production hook already exists at exactly the needed point
   (`"before_commit_append"`, fired right after the redirect-stub content rename, before WAL
   commit) — meaning no NEW production code is required.
3. Confirm `engine/catalog/content.go`'s `ContentStore.Read` reads disk content directly,
   gated only on `cat.Get` existing (not on Status) — so the rename instant is exactly the
   moment a raw `cs.Read` call becomes guaranteed to see post-split bytes.
4. Confirm no `t.Parallel()` in `engine/split/*.go` (safe to mutate the shared
   `atomicCommitHook` var scoped to this one test, restored via `defer`).
5. Edit `TestReaderDuringSplit`:
   a. Install `atomicCommitHook` to close `renamedCh` (via `sync.Once`) at stage
      `"before_commit_append"`; restore to `nil` via `defer`.
   b. Change the background reader loop from a fixed 400-iteration bound to a
      `stopCh`-terminated loop (with a defensive 2,000,000-iteration cap), preserving its
      role as the genuine concurrent "never observe a torn read" exerciser.
   c. After `<-splitDone`, `<-renamedCh` (already closed by then; makes happens-before
      explicit), then perform ONE deterministic `cs.Read(fileID)` guaranteed-sequenced after
      the rename, fold it into the same `reads` slice.
   d. `close(stopCh)`, `<-readerDone`, drain `readerErrCh` as before.
   e. Leave the rest of the test (exact-match validation loop, `sawPostSplit == 0` check)
      unchanged — it now passes deterministically because of the guaranteed extra read.
   f. Update the test's doc comment to explain the fix (why it flaked, what barrier replaces
      the sleep assumption, why the torn-read half of the contract is unaffected).
6. Run `go vet ./engine/split/...`.
7. Run `go test ./engine/split/... -race -run TestReaderDuringSplit -count=50` (spec-required)
   and `-count=100` for extra confidence.
8. Run `go test ./engine/split/... -race` (package-scoped full suite).
9. Confirm via `git status --short` that only `split_race_test.go` differs (relative to other
   concurrent agents' pre-existing changes to guard.go/orchestrate.go etc., which are not
   mine and must not be staged).
10. Stage exactly `engine/split/split_race_test.go`; commit with Problem/Solution/Impact
    message; write self-consistency.json and handoff.json.
