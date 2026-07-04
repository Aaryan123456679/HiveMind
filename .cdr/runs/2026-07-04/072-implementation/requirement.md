# Requirement — Subtask 2a.1.3 (Issue #6)

Verbatim from `gh issue view 6`:

- **2a.1.3 — Snapshot-read path: capture pointer at request start, read that version to completion**
  - Acceptance criteria: A reader that starts before a concurrent write commits continues to see its
    originally-snapshotted version for the entire read, even after the pointer advances.
  - Test spec: `go test ./engine/mvcc/... -run TestSnapshotRead -race`: start a read, trigger a
    concurrent write mid-read, assert the read completes against the pre-write version content.
  - Impacted modules: `engine/mvcc/read.go, engine/mvcc/read_test.go`

Prior subtasks done and verified:
- 2a.1.1: `VersionWriter` (engine/mvcc/write.go) — immutable `content/<fileID>.vN.md`, monotonic
  per-fileID numbering, never rewrites/reuses a version once assigned.
- 2a.1.2: `Catalog.CompareAndSwapCurrentVersion` (engine/catalog/catalog.go) +
  `VersionWriter.CommitVersion` (engine/mvcc/write.go) — durable-write-then-CAS-publish, retry-on-lost-race,
  no lost updates.
