# Subtask 1.5.2 (GitHub issue #5, last subtask)

End-to-end crash-recovery integration test across all four modules
(catalog, btree, content, wal).

**Acceptance criteria**: Killing the process mid-append (simulated) and
restarting reconstructs a consistent catalog+btree+content state via WAL
replay, with no partial/corrupted file visible.

**Test spec**: `go test ./engine/... -run TestStorageCoreCrashRecovery -race`:
interrupt an append mid-way, reopen all stores, run WAL recovery, assert
consistent end state across catalog/btree/content.

**Impacted modules**: `engine/integration_test.go` (extend, add new test
function).
