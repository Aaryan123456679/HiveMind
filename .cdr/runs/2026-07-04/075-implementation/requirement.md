# Subtask 2a.1.4 — WAL integration for version-pointer CAS (issue #6)

Acceptance criteria: Every version-pointer CAS is logged to the WAL before being
applied, consistent with the catalog-mutation invariant in wal.md.

Test spec: `go test ./engine/mvcc/... -run TestVersionCASWAL -race`: assert WAL
record precedes pointer visibility; crash-inject mid-CAS and confirm recovery
reconstructs a valid pointer.

Impacted modules: engine/mvcc/write.go, engine/mvcc/write_test.go.

Context: subtasks 2a.1.1 (VersionWriter), 2a.1.2 (CompareAndSwapCurrentVersion /
CommitVersion — explicitly NOT WAL-safe yet, must not be used by real callers
until this subtask), 2a.1.3 (Snapshot/Read) are done and verified.
