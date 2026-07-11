# Requirement — Subtask 4.5.4.3 (Issue #41)

## Title
Add RecordTypeInvalid / out-of-range RecordType validation in DecodeTypedRecord

## Source
GitHub issue #41, subtask 4.5.4.3 (`.cdr/memory/pending.md` / `.cdr/index/regression.jsonl`
open item: "RecordTypeInvalid validation gap").

## Acceptance criteria
`DecodeTypedRecord` (engine/wal/record.go) must reject:
1. `RecordTypeInvalid` (byte value 0), and
2. Any unrecognized/out-of-range `RecordType` byte (i.e. any value not equal to one
   of the currently-defined non-zero constants: RecordCatalogPut(1),
   RecordCatalogDelete(2), RecordBTreeInsert(3), RecordBTreeDelete(4),
   RecordSplitCommit(5)),

with an explicit, descriptive error — matching the existing `InvalidFileID` /
`reservedNodeID` runtime-guard convention used elsewhere in the codebase
(engine/catalog/catalog.go's `if rec.FileID == InvalidFileID { return fmt.Errorf(...) }`
style: an early, explicit guard clause returning a `fmt.Errorf` with package-prefixed
message) — instead of silently decoding a corrupted/garbage record into a
seemingly-valid `TypedRecord`.

## Test spec
`go test ./engine/wal/... -run TestDecodeTypedRecordRejectsInvalidType`:
construct a record with RecordType 0 (RecordTypeInvalid) and one with an
out-of-range value (> highest defined RecordType, i.e. > RecordSplitCommit),
assert both are rejected with explicit, non-nil errors. Additionally, no
existing valid RecordType constant must be rejected (regression check across
the full existing round-trip test suite).

## Impacted modules
- engine/wal/record.go
- engine/wal/record_test.go

## Scope guard
Only files under engine/wal/ are in scope for this run. engine/btree/persist.go
(subtask 4.5.4.1) and engine/wal/checkpoint.go (subtask 4.5.4.4) are explicitly
out of scope.
