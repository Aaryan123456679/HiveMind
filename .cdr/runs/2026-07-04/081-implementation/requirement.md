# Requirement (subtask 2a.2.1, GitHub issue #7 "Epoch-based garbage collection", engine/mvcc/)

Parent task: task-2a.2 (second task of Epic Phase 2a). First subtask under task-2a.2.
Prerequisite task-2a.1 (VersionWriter, CAS, Snapshot/Read, WAL-logged CAS, concurrent race
test — engine/mvcc/write.go, read.go) is fully done and verified.

## Subtask 2a.2.1 — Snapshot epoch refcounting (increment on start, decrement on completion)

- Acceptance criteria: Each snapshot increments its epoch's refcount on creation and
  decrements on completion; refcounts never go negative and reach zero once all
  referencing readers finish.
- Test spec: `go test ./engine/mvcc/... -run TestEpochRefcount -race`: open/close
  overlapping snapshots across epochs, assert refcount bookkeeping is correct at every
  step.
- Impacted modules: `engine/mvcc/gc.go` (new), `engine/mvcc/gc_test.go` (new).
