# Plan — Subtask 1.1.1

1. Create `engine/catalog/record.go`:
   - `RecordStatus uint8` type with constants `StatusActive, StatusSplitting, StatusSplit,
     StatusRedirect` (0-3).
   - `MaxRedirectTargets = 8` constant.
   - `CatalogRecord` struct: `FileID uint64`, `PathHash uint64`, `CurrentVersion uint64`,
     `SizeBytes uint64`, `Status RecordStatus`, `RedirectTargetIDs []uint64` (logical,
     len <= MaxRedirectTargets), `ParentTopicID uint64`, `LastModified int64` (Unix nanos).
   - `RecordEncodedSize` constant = exact byte length of the fixed layout.
   - `Encode() []byte` — writes all fields little-endian into a
     `RecordEncodedSize`-byte slice: fileID(8) + pathHash(8) + currentVersion(8) +
     sizeBytes(8) + status(1) + redirectCount(1) + padding(2, reserved/zero) +
     redirectTargetIDs(8*MaxRedirectTargets, zero-padded past count) + parentTopicID(8) +
     lastModified(8).
   - `Decode(data []byte) (CatalogRecord, error)` — validates `len(data) ==
     RecordEncodedSize`, validates redirect count <= MaxRedirectTargets, reconstructs the
     struct with `RedirectTargetIDs` truncated/allocated to exactly `count` length (nil/empty
     slice when count==0, so zero-value round-trips exactly).
2. Create `engine/catalog/record_test.go`:
   - Table-driven `TestRecordEncodeDecode` with race flag support (no goroutines needed but
     must pass under `-race` harmlessly):
     - Case "zero value": all-zero record incl. empty RedirectTargetIDs, Status=StatusActive.
     - Case "populated": non-zero fields for every field, RedirectTargetIDs with 3 entries,
       Status=StatusSplit.
     - Case "max redirect targets": RedirectTargetIDs at exactly MaxRedirectTargets length.
   - For each case: `enc := rec.Encode()`, assert `len(enc) == RecordEncodedSize`, then
     `got, err := Decode(enc)`, assert `err == nil` and `reflect.DeepEqual(got, rec)` (with
     RedirectTargetIDs normalized to nil for empty case to match Decode's convention).
   - Additional negative-path checks: `Decode` on wrong-length buffer returns error;
     Decode is deterministic (no data loss) is exercised by the round-trip equality itself.
3. Run `go build ./engine/...` and
   `go test ./engine/catalog/... -run TestRecordEncodeDecode -race -v` to self-check.
4. Write self-consistency.json + handoff.json, update indexes, commit locally (no push).
