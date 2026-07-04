# Plan — Subtask 1.3.4

## New file: engine/wal/recovery.go

1. `func Replay(dir string, apply func(TypedRecord) error) error`
   - `LoadCheckpoint(dir)` → `(segNum, offset, found, err)`; if `!found`,
     `segNum, offset = 0, 0`.
   - `listSegmentNumbers(dir)` → all `wal-<N>.log` numbers in `dir`, sorted
     ascending. If empty, return `nil` immediately (fresh WAL, nothing to
     replay — a correct no-op).
   - If `segNum > lastSegmentNumber`, return a hard error (inconsistent
     checkpoint state).
   - For each segment number `n` in ascending order with `n >= segNum`:
     - `startOffset := 0` unless `n == segNum`, in which case
       `startOffset := offset`.
     - `records, err := readSegmentFrom(segmentPath(dir, int(n)), startOffset)`.
     - For each record (in on-disk order): `DecodeTypedRecord`, validate
       `Type` via `isValidRecordType`, call `apply(rec)` if non-nil, in
       order, exactly once. First error from decode/validate/apply
       short-circuits and is returned wrapped with segment context.
   - Segments with `n < segNum` are skipped entirely (their mutations are
     already covered by the checkpoint and archivable per checkpoint.go's
     `ArchivableSegments` semantics — must not be double-applied).

2. `func readSegmentFrom(path string, startOffset int64) ([][]byte, error)`
   - Mirrors `ReadSegment`'s parsing loop (writer.go) exactly, but the loop
     starts at `off := startOffset` instead of `off := 0`. Reuses the same
     `recordHeaderSize`/`offRecordLength`/`offRecordCRC` constants (same
     package, already defined in writer.go). Same error conditions
     (truncated header, truncated payload, CRC mismatch) as `ReadSegment`.

3. `func listSegmentNumbers(dir string) ([]uint64, error)`
   - Scans `dir` for `wal-<N>.log` files (same prefix/suffix constants as
     writer.go/checkpoint.go), parses `N` as `uint64`, returns all numbers
     found, sorted ascending, ignoring non-matching filenames (consistent
     with `latestSegmentNum`/`ArchivableSegments`'s existing tolerance for
     unrelated files in the same directory).

4. `func isValidRecordType(t RecordType) bool`
   - Returns true only for `RecordCatalogPut`, `RecordCatalogDelete`,
     `RecordBTreeInsert`, `RecordBTreeDelete`. False for
     `RecordTypeInvalid` (0) and any other byte value. This is the gap
     closure point per the regression note.

## New file: engine/wal/recovery_test.go

- `TestRecoveryReplay` (required by the test spec's exact `-run` name):
  pre-populate a WAL directory (via `OpenWriter` + `AppendAndApply`/
  `Writer.Append` of `TypedRecord.Encode()`s) spanning multiple segments
  (small `maxSegmentBytes` to force rotation), checkpoint partway through
  (leaving records past the checkpoint), then:
  - (a) Call `Replay` with a test-local fake `apply` that appends each
    received `TypedRecord` to a slice. Assert the replayed slice exactly
    equals the sub-sequence of originally-appended records from the
    checkpoint offset forward (by segment+offset bookkeeping kept in the
    test), in original order, exactly once — i.e., records before the
    checkpoint are NOT present in the replayed slice.
  - (b) Cross-check "final state matches applying the same mutations
    directly" per the test spec: apply all ORIGINAL records directly (in
    full, from the start) into a reference in-memory map (simulating
    "final state"), then apply only the REPLAYED records on top of a copy
    of state that already reflects everything up to the checkpoint, and
    assert both converge to the same final map. This operationalizes
    "assert final state matches applying the same mutations directly"
    using the test-local fake state since real catalog/btree wiring is out
    of scope (see architecture-discovery.md's Scope boundary section).
- `TestRecoveryReplayNoOpWhenCheckpointCoversEverything`: checkpoint at the
  exact end of the last segment (segment number = last segment, offset =
  that segment's on-disk size via `os.Stat`), call `Replay`, assert `apply`
  is never invoked (call counter stays 0) and `Replay` returns nil.
- `TestRecoveryReplayNoCheckpointStartsFromBeginning`: no `Checkpoint` call
  at all (fresh WAL, `LoadCheckpoint` found=false case); `Replay` must
  replay every record from segment 0 offset 0.
- `TestRecoveryReplayInvalidRecordType`: hand-craft a segment containing one
  record whose `TypedRecord.Encode()`-shaped bytes have an invalid/
  unrecognized `RecordType` byte (e.g. `0` or `99`), written via
  `Writer.Append` directly (bypassing `NewCatalogPutRecord`/etc. helpers so
  the invalid byte survives), and assert `Replay` returns a non-nil error
  (not a silent skip, not a silent success) — this is the genuine test for
  the closed 1.3.2 gap, not just a comment.

## Self-consistency (internal only, per I4 — no verification claims)

- `go build ./engine/...`
- `go vet ./engine/...`
- `go test ./engine/wal/... -race -v -count=1` (must show all of 1.3.1's,
  1.3.2's, 1.3.3's existing tests plus the new recovery_test.go tests
  passing, confirming no regression).
