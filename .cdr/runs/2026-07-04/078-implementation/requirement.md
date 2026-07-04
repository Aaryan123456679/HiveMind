# Requirement (verbatim, subtask 2a.1.5 of GitHub issue #6)

## Subtask 2a.1.5 — Concurrent writer/reader race test, no torn reads
- **Acceptance criteria**: Many concurrent readers and writers against the same fileID
  never observe a torn/partial version; every read returns exactly the full content of
  some committed version.
- **Test spec**: `go test ./engine/mvcc/... -run TestConcurrentReadersWriters -race`: N
  writer goroutines committing distinct versions concurrently with M reader goroutines
  taking snapshots and reading, assert every read's content exactly matches one of the
  committed payloads (no corruption, no mixing).
- **Impacted modules**: `engine/mvcc/mvcc_test.go` (new file — an integration-style test
  across `write.go` + `read.go`, not unit-testing either in isolation).

This is the LAST subtask under task-2a.1 (MVCC versioned writes), issue #6. Subtasks
2a.1.1 (VersionWriter), 2a.1.2 (CompareAndSwapCurrentVersion/CommitVersion), 2a.1.3
(Snapshot/Read), and 2a.1.4 (WAL-logged CAS) are all done and verified.
