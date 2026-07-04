# Requirement — Subtask 1.3.5: Crash-injection recovery test

Source: `gh issue view 3`, checklist item 1.3.5 (verbatim):

> - [ ] **1.3.5 — Crash-injection recovery test (mid-write crash simulation)**
>   - Acceptance criteria: Simulating a crash after a partial/torn record write
>     does not corrupt recovery; the torn record is detected and discarded,
>     and recovery proceeds from the last valid record.
>   - Test spec: `go test ./engine/wal/... -run TestCrashInjectionRecovery -race`:
>     truncate/corrupt the tail of a segment mid-record, run recovery, assert
>     clean state with no panic and no corrupted-record replay.
>   - Impacted modules: `engine/wal/recovery_test.go`

This is the last subtask under GitHub issue #3 (Epic: Phase 1 — Storage core).
1.3.1–1.3.4 are all `verified`. Closing this subtask should make issue #3
closable.

## Deferred gaps this subtask must close (per orchestrator instructions,
cross-referenced against `.cdr/index/task.jsonl` / prior verification runs)

- (a) 1.3.1: `OpenWriter`'s resume path does not validate a resumed
  segment's tail for torn/incomplete records.
- (b) 1.3.2: `TestFsyncBeforeApply` only proves ordering within one process
  (page-cache-coherent same-process reads), not literal fsync-to-disk
  durability across a real process boundary.
- (c) 1.3.4: no test exercises a record with a valid length but corrupted
  payload bytes (bad CRC) end-to-end through `Replay`.
- (d) `Writer` lacks a public getter for its current segment offset/size,
  which a real checkpoint caller (`Checkpoint(dir, segNum, offset)`) needs.

## Reconciling the literal acceptance criteria with the gaps

The issue's own acceptance criteria is explicit: a torn tail must be
"detected and discarded", and "recovery proceeds" (not: recovery hard-errors
and refuses to continue). This is the ground truth for how a **torn** record
(incomplete header or incomplete payload — the only shapes a crash mid-Append
can leave) must be handled, in both `OpenWriter`'s resume path and `Replay`.

Gap (c)'s CRC-corruption case is a *different* failure mode: a full-length
record with a bad checksum. A crash mid-write cannot produce that (it can
only leave a record short, never full-length-but-bit-flipped), so a CRC
mismatch indicates genuine corruption, not an incomplete write. Per gap (c)'s
own wording ("assert Replay returns a clear error... not silent data
corruption"), this case must remain a hard, visible error — never silently
discarded like a torn tail.

Design decision (see plan.md / architecture-discovery.md for detail):
distinguish "torn" (not enough bytes present — silently discard/truncate,
recovery proceeds) from "corrupt" (enough bytes present, CRC fails — hard
error) as two structurally different, consistently-handled cases across
`OpenWriter`, `ReadSegment`, and `Replay`.
