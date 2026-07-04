# Requirement — Subtask 1.3.4

Source: GitHub issue #3 (Epic "Phase 1: Storage core"), checklist item 1.3.4, verbatim.

## Acceptance criteria
On startup, replay reapplies every logged-but-not-yet-checkpointed mutation, in
order, exactly once, and is a no-op if the checkpoint pointer already covers
all segments.

## Test spec
`go test ./engine/wal/... -run TestRecoveryReplay`: pre-populate WAL with
mutations past the checkpoint, run recovery, assert final state matches
applying the same mutations directly.

## Impacted modules
`engine/wal/recovery.go`, `engine/wal/recovery_test.go`

## Additional closure requirement (carried from 1.3.2's verification, run
038-verification, regression.jsonl)
`DecodeTypedRecord` performs no validation of `RecordType` (accepts
`RecordTypeInvalid=0` and any unrecognized type byte silently). Flagged as
"close before 1.3.4's recovery replay dispatches on Type directly." This
subtask's `Replay` function must reject `RecordTypeInvalid` and any
unrecognized `RecordType` byte with an error, not silently skip or succeed.
