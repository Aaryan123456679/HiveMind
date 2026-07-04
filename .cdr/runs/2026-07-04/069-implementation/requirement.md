# Requirement — Subtask 2a.1.2 (GitHub issue #6)

Verbatim from issue #6 (MVCC epic, subtask 2a.1.2):

## Subtask 2a.1.2 — Atomic CAS swap of catalog's current-version pointer post-durable-write

- **Acceptance criteria**: The catalog's `currentVersion` field is updated via CAS only
  after the new version file is durably written; a failed CAS (concurrent writer won)
  retries without corrupting state.
- **Test spec**: `go test ./engine/mvcc/... -run TestCurrentVersionCAS -race`: concurrent
  writers race to CAS, assert exactly the expected final currentVersion and no lost
  updates.
- **Impacted modules**: `engine/mvcc/write.go`, `engine/mvcc/write_test.go`.

Builds on subtask 2a.1.1 (`engine/mvcc/write.go`'s `VersionWriter`, writes immutable
`content/<fileID>.vN.md` with monotonic per-fileID numbering) — done and verified
(commit 34bc95d, closed out 1cdcabe).
