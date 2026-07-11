# Plan — Subtask 4.5.4.3

## Change to engine/wal/record.go

In `DecodeTypedRecord`, after the existing length guard, add:

```go
func DecodeTypedRecord(data []byte) (TypedRecord, error) {
	if len(data) < recordTypeSize {
		return TypedRecord{}, fmt.Errorf("wal: record too short to contain a type tag: got %d bytes, need at least %d", len(data), recordTypeSize)
	}
	t := RecordType(data[0])
	if t == RecordTypeInvalid || t > RecordSplitCommit {
		return TypedRecord{}, fmt.Errorf("wal: decode: invalid record type %d", byte(t))
	}
	return TypedRecord{
		Type:    t,
		Payload: data[recordTypeSize:],
	}, nil
}
```

Rationale for `t > RecordSplitCommit` as the upper bound: `RecordSplitCommit`
(5) is the highest currently-defined constant; any byte value greater than it
is by definition out-of-range/unrecognized. This mirrors the `InvalidFileID`
guard style (`if rec.FileID == InvalidFileID { return fmt.Errorf("catalog: put: invalid fileID %d", rec.FileID) }`):
early guard clause, package-prefixed message (`wal: decode: ...`), "invalid
record type %d" wording paralleling "invalid fileID %d".

Using a range check (`t > RecordSplitCommit`) rather than an exhaustive
switch/allowlist keeps the guard low-maintenance-cost as new RecordType
constants are added in numeric sequence (matching how the enum itself is
defined as a contiguous 0..5 range) while still catching every currently
out-of-range byte. This is a reasonable, minimal choice given the enum's
existing contiguous-byte-value design; if the enum ever became non-contiguous
this would need revisiting, but that is out of scope here.

## New test in engine/wal/record_test.go

Add `TestDecodeTypedRecordRejectsInvalidType`:
- Subtest "zero value": build `[]byte{0, 'x'}` (type byte 0 + arbitrary
  payload byte so length guard doesn't fire), call `DecodeTypedRecord`,
  assert `err != nil`.
- Subtest "out of range": build `[]byte{6, 'x'}` (one past RecordSplitCommit)
  and `[]byte{255, 'x'}`, call `DecodeTypedRecord`, assert `err != nil` for
  both.
- Also assert all 5 currently-valid constants (RecordCatalogPut..
  RecordSplitCommit) still decode without error, as an inline regression
  guard co-located with the new test (in addition to running the full
  existing suite).

## Self-consistency checks (not verification)

1. `gofmt -l engine/wal/record.go engine/wal/record_test.go` clean.
2. `go vet ./engine/wal/...` clean.
3. `go test ./engine/wal/... -run TestDecodeTypedRecordRejectsInvalidType -v` passes.
4. `go test ./engine/wal/... -race -v` full suite passes, zero regressions.
