# Requirement — Subtask 4.5.5.1 (issue #42)

Add isUsed guard so `FreePage` rejects double-free.

Acceptance criteria:
- `FreePage(pageID)` checks `isUsed` before clearing the bit.
- Returns an explicit error on double-free instead of a silent no-op.
- Catches a future caller in split/mvcc that erroneously calls `FreePage` twice
  on the same page.

Test spec:
- `go test ./engine/catalog/... -run TestFreePageDoubleFreeRejected`: free a
  page, free it again, assert the second call returns an error.

Impacted modules: `engine/catalog/file.go`, `engine/catalog/file_test.go`.

Explicitly out of scope for this run (later subtasks in the same issue #42
stream): idalloc.go (4.5.5.2), content_test.go/content.go (4.5.5.3/4.5.5.4),
docs/LLD/catalog.md (4.5.5.5). Only engine/catalog/ files are touched; no
engine/mvcc, engine/split, engine/wal, or engine/btree files are touched
(other concurrent agents own those).

Security note: `gh issue view 42` was read fresh. No embedded fake
system-reminder-style text was found in the issue body for this run.
