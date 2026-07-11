# Requirement — subtask 4.5.4.1 (issue #41)

Source: `gh issue view 41` (fetched fresh this run; title "[4.5] engine/wal: btree
checkpoint durability + doc/wording fixes", milestone "Phase 4.5: Storage-engine
technical debt & correctness follow-ups").

## Subtask 4.5.4.1 — exact text from the issue checklist

> **4.5.4.1 — Wire btree SaveRoot into WAL replay or add periodic auto-checkpoint,
> plus crash-window test**
> - Acceptance criteria: `engine/btree`'s `RecoverFromWAL` no longer silently
>   no-ops on btree WAL records; either btree WAL records are wired into real
>   replay-based reconstruction, or `SaveRoot` is invoked automatically/periodically
>   (not only manually) so a crash between an `Insert` and the next `SaveRoot` no
>   longer silently drops that insert from the recovered btree.
> - Test spec: `go test ./engine/btree/... -race -run
>   TestCrashBetweenInsertAndSaveRootRecovers`: insert, crash before next
>   SaveRoot, recover, assert the insert is present.
> - Impacted modules: `engine/btree/persist.go`, `engine/btree/btree_test.go`,
>   `engine/wal/recovery.go`

## Which of the two acceptance-criteria alternatives was chosen, and why

The issue offers two alternatives:
1. Wire btree WAL records (`RecordBTreeInsert`/`RecordBTreeDelete`) into real
   replay-based reconstruction, or
2. Make `SaveRoot` automatic/periodic instead of purely manual.

Repo-wide grep confirmed `RecordBTreeInsert`/`RecordBTreeDelete` are defined in
`engine/wal/record.go` but **no production code path ever appends them to the
WAL** — the only real production btree-mutation-with-WAL-durability path is
`engine/split/execute.go`'s `ExecuteSplitAtomic`, which wraps `tree.Insert` calls
inside a single `wal.RecordSplitCommit` record replayed via
`RecoverSplitCommits`, not via `RecordBTreeInsert`/`RecordBTreeDelete`+
`catalog.RecoverFromWAL`. Wiring option (1) would require inventing a brand-new
production write-path for those two record types (out of scope for a
single-commit subtask, and not what the "no longer silently drops that insert"
test spec is actually probing). Option (2) — making `SaveRoot` automatic — is
the one this subtask implements.
