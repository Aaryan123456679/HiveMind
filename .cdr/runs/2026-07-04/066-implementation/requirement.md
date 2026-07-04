# Requirement (subtask 2a.1.1, issue #6 "MVCC versioned write/read path")

## Subtask 2a.1.1 — Version file writer: content/<fileID>.vN.md creation with monotonic version numbering
- Acceptance criteria: Each write creates a new immutable version file with N strictly
  increasing per fileID; prior version files are left untouched.
- Test spec: `go test ./engine/mvcc/... -run TestVersionWriter -race`: perform sequential
  and concurrent writes to the same fileID, assert version numbers are strictly increasing
  with no collisions.
- Impacted modules: `engine/mvcc/write.go`, `engine/mvcc/write_test.go` (new package).

Scope: JUST the version-file-writer. No WAL/CAS/catalog integration (that is 2a.1.2/2a.1.4).
