# Architecture Discovery — Subtask 4.5.4.3

## Current state of engine/wal/record.go

- `RecordType byte` enum (lines 22-55):
  - `RecordTypeInvalid RecordType = 0` — documented as "the zero value and
    never a valid on-disk record type."
  - `RecordCatalogPut = 1`
  - `RecordCatalogDelete = 2`
  - `RecordBTreeInsert = 3`
  - `RecordBTreeDelete = 4`
  - `RecordSplitCommit = 5`
- `(RecordType).String()` (lines 58-73) already has an exhaustive switch over
  the 5 valid constants with a `default: fmt.Sprintf("RecordType(%d)", byte(r))`
  fallback for unknown values — useful for error message formatting, no need
  to duplicate the name list.
- `DecodeTypedRecord` (lines 103-113):
  ```go
  func DecodeTypedRecord(data []byte) (TypedRecord, error) {
      if len(data) < recordTypeSize {
          return TypedRecord{}, fmt.Errorf("wal: record too short to contain a type tag: got %d bytes, need at least %d", len(data), recordTypeSize)
      }
      return TypedRecord{
          Type:    RecordType(data[0]),
          Payload: data[recordTypeSize:],
      }, nil
  }
  ```
  Currently only guards on length; no validation at all on the decoded
  `RecordType` byte value. A garbage/corrupted leading byte (0, or 6+) decodes
  silently into a `TypedRecord` with an invalid `Type`, which downstream
  callers dispatch on positionally (recovery.go / catalog.RecoverFromWAL /
  split.RecoverSplitCommits) — this is exactly the "corrupted/garbage record"
  hazard the enum's own doc comment (lines 16-21) already calls out.

## Existing runtime-guard convention (InvalidFileID / reservedNodeID)

`engine/catalog/catalog.go` `Put`:
```go
func (c *Catalog) Put(rec CatalogRecord) error {
    if rec.FileID == InvalidFileID {
        return fmt.Errorf("catalog: put: invalid fileID %d", rec.FileID)
    }
    ...
```
Same guard style repeated at other call sites in catalog.go (Get/Delete
paths) — early `if x == sentinel { return fmt.Errorf("<pkg>: <op>: invalid <field> %v", x) }`
guard clause, package name as message prefix, followed by operation name,
then a short "invalid X: %v"-shaped description.

`engine/btree` mirrors this with `reservedNodeID` (0) as its sentinel,
guarding root/lookup entry points (`lookup.go`, `insert.go`, `delete.go`)
rather than at decode time, since btree's sentinel represents "empty tree"
(a valid state), not corruption — not a direct analog for the decode-time
validation needed here, but confirms the "guard on sentinel with explicit
error, `pkg: op: invalid X %v`" wording convention used repo-wide.

## Test conventions (engine/wal/record_test.go)

- Uses plain `testing.T`, `t.Run` subtests, `t.Fatalf` on unexpected errors.
- `TestAsXxxTypeMismatch` (line 201) is the closest existing precedent for a
  "must return an explicit error, not silently succeed" test: builds a record
  of one Type, calls the wrong `AsXxx` accessor, asserts an error is returned
  (via `errors.Is`/`errors.As` or just `err == nil` check — to be confirmed
  from the function body before writing the new test in the same idiom).
- No existing test constructs a raw byte record manually to test
  `DecodeTypedRecord` corruption paths (the "too short" case is presumably
  covered by a different existing test, or not covered at all — will check).

## Plan implication

Add a single guard clause in `DecodeTypedRecord`, right after the length
check and before constructing the `TypedRecord`, validating
`RecordType(data[0])` against the known set. Use `RecordType.String()`'s
existing "RecordType(%d)" fallback for the value in the error message body,
matching the `pkg: op: invalid X %v` convention: something like
`fmt.Errorf("wal: decode: invalid record type %s", t)` reusing `.String()`,
or `fmt.Errorf("wal: decode: invalid or out-of-range record type %d", data[0])`
directly — final wording decided in plan.md.
