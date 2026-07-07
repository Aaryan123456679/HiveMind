# Plan

1. Rewrite `TestReaderDuringSplit` in `engine/split/split_race_test.go` (Option A):
   - Seed `fileID` via `cs.Create` with real pre-split content (no separate `mvcc` root at all).
   - Launch a reader goroutine that repeatedly calls `cs.Read(fileID)` (the SAME `ContentStore`
     `ExecuteSplitAtomic` mutates) throughout the split window, recording a copy of each observed
     read (not judging validity inline, since the real post-split fileID/RedirectTargetIDs are only
     known after the split completes).
   - Concurrently drive a REAL `orch.BeginSplit` + `ExecuteSplitAtomic` against the same `cs`/fileID.
   - After both goroutines finish: fetch the real `RedirectTargetIDs` from `cat.Get(fileID)`, compute
     the exact expected stub bytes via the production `buildRedirectStubContent` helper (same
     package), and assert every recorded read is byte-identical to EITHER the known pre-split content
     OR the exact expected stub -- fail loudly on anything else (torn/garbage read).
   - Assert the reader observed the post-split state at least once (proves real overlap, not just
     a trivially-fast split).
   - Remove the now-unused `mvcc` import.
2. Confirm the test has real teeth: temporarily reintroduce a torn/non-atomic write in
   `split/execute.go`'s `writeNewContentFile` (truncate + partial write + sleep + partial write, no
   rename), rerun `TestReaderDuringSplit -race -count=5`, confirm FAIL with torn-content messages,
   then revert `execute.go` back to its exact original (`3e95aa2`) state via `cp` from a backup taken
   before the injection, and confirm `go vet`/`gofmt` clean again.
3. Run full self-consistency matrix (see self-consistency.json).
4. One local commit (no push), Problem/Solution/Impact style, explicitly a fix cycle for CHANGES_REQUESTED.
5. handoff.json with pointers only, noting Option A was taken and why.
