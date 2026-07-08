# Requirement (fix cycle for subtask 3.1.3, issue #15)

Source: `.cdr/runs/2026-07-08/008-verification/verification.json`, verdict
CHANGES_REQUESTED, blocking finding F1 (critical), commit reviewed
`ebbc1ff0d24d641ec870cdc0a452149da2c25a4c`.

## Confirmed bug

`engine/graph/compact.go`'s `Compact()`: append one `EdgeEntityCooccur` edge
(Weight=3) -> run `Compact` with `TruncateNode` forced to fail AFTER
`WriteCSR`'s rename succeeds (graph.dat correctly shows Weight=3) -> run
`Compact` again (the documented recovery action) -> graph.dat now incorrectly
shows Weight=6. Root cause: `Compact()` loads the already-updated graph.dat as
`existing`, then re-reads the still-un-truncated log entry as `incoming`, and
the `EdgeEntityCooccur` merge branch sums `existing.Weight + incoming.Weight`
- but `existing` already includes that log entry's contribution from the
prior successful compaction. Not self-correcting: permanently corrupts the
durable weight, compounding further on each subsequent failed-truncate retry
(6 -> 9 -> 12 ...).

## Fix requirement

Make compaction idempotent under retry after a truncate-phase failure: a
second `Compact()` call following a successful `graph.dat` write but failed
truncation must NOT re-apply already-durably-merged edge-log entries.

1. Root-cause fix in `engine/graph/compact.go` and `engine/graph/edgelog.go`.
2. New regression test forcing the exact failure window, then performing a
   second `Compact()` call, asserting weight is unchanged after retry.
3. Existing `TestCompaction` subtests, `TestTruncateNode`, and both existing
   crash-injection tests pass unmodified in spirit.
4. Do not weaken crash-safety already verified correct in the prior pass
   (crash-before-rename still leaves old graph.dat + full logs untouched);
   the fix should only affect the crash-after-rename-before-truncate window.
