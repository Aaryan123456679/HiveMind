# Subtask 1.4.4 — Durability round-trip test across simulated restart

(verbatim, from gh issue view 4 subtask list + task prompt)

Part of Epic: Phase 1: Storage core (single-threaded). Issue #4: "[1] Single-version
.md content read/write (engine/catalog/ content I/O, pre-MVCC)".

- **Acceptance criteria**: After writing/appending content, simulating a process restart
  (reopen WAL + catalog + content store from disk) and reading returns the same content
  as before the restart.
- **Test spec**: `go test ./engine/catalog/... -run TestContentDurabilityRestart -race`:
  write/append, simulate restart via WAL replay, assert content matches.
- **Impacted modules**: `engine/catalog/content_test.go`, possibly `engine/wal/recovery.go`
  if any gap is found (should not need engine/wal changes — it's already verified — but
  check).

This is the last subtask of issue #4; closes out the issue once verified.
