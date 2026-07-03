# Plan — Subtask 1.3.2

1. `engine/wal/record.go`:
   - `RecordType byte` enum: `RecordCatalogPut = 1`, `RecordCatalogDelete = 2`,
     `RecordBTreeInsert = 3`, `RecordBTreeDelete = 4` (start at 1, reserve 0 as invalid/zero-value
     sentinel, matching the repo's general avoidance of a meaningful zero value for enums used in
     on-disk formats).
   - Per-kind payload structs + Encode/Decode:
     - `CatalogPutPayload{FileID uint64, Record []byte}` — Record is the caller-supplied, already
       -encoded `catalog.CatalogRecord` bytes (this package does not import engine/catalog to
       avoid a cross-module dependency; it treats the encoded record as an opaque length-prefixed
       blob, matching the "WAL doesn't know record semantics beyond what it needs to replay"
       principle — recovery, which does need catalog semantics, is where that import belongs).
     - `CatalogDeletePayload{FileID uint64}`.
     - `BTreeInsertPayload{Path string, FileID uint64}`.
     - `BTreeDeletePayload{Path string}`.
     - Encoding: fixed-width fields little-endian; variable-width fields (Path, Record bytes)
       length-prefixed with a uint32 length header, consistent with writer.go's own
       length-prefix idiom.
   - `TypedRecord{Type RecordType, Payload []byte}` with `Encode() []byte` / `DecodeTypedRecord
     (data []byte) (TypedRecord, error)` — a 1-byte type tag followed by the kind-specific
     payload bytes (no extra length prefix needed here since Writer.Append's own header already
     carries the total payload length).
   - Convenience constructors: `NewCatalogPutRecord(fileID uint64, encodedRecord []byte)
     TypedRecord`, `NewCatalogDeleteRecord(fileID uint64) TypedRecord`,
     `NewBTreeInsertRecord(path string, fileID uint64) TypedRecord`,
     `NewBTreeDeleteRecord(path string) TypedRecord` — each producing a ready-to-append
     `TypedRecord`, plus paired `AsCatalogPut() (CatalogPutPayload, error)` -style decode
     accessors dispatched on `Type`.
   - `AppendAndApply(w *Writer, rec TypedRecord, apply func() error) (offset int64, err error)`:
     1. `payload := rec.Encode()`
     2. `offset, err = w.Append(payload)` — this call is where 1.3.1's fsync happens; if it
        errors, return immediately without calling `apply`.
     3. On success, call `apply()`. If `apply` returns an error, propagate it as-is (wrapped with
        context) but still return the valid `offset` (non-zero-value now meaningfully "this much
        was durably logged even though apply failed") alongside the error — callers must check
        `err` but the offset remains informative for diagnostics/tests.
   - Doc comment on `AppendAndApply` spelling out the durability contract and the apply-failure
     semantics (matches requirement point 4).
2. `engine/wal/record_test.go`:
   - Round-trip encode/decode test per record kind (`TestCatalogPutRoundTrip`, etc., or one
     table-driven `TestRecordEncodeDecodeRoundTrip`).
   - `TestFsyncBeforeApply`: use a real `Writer` (temp dir), call `AppendAndApply` with an `apply`
     callback that appends an event to a shared `[]string` events slice; separately, use a small
     wrapper/spy around the fsync step — since `Writer.Append` doesn't expose a hook, prove
     ordering by recording "append-returned" as an event immediately after `w.Append` returns
     inside `AppendAndApply` itself is not observable externally, so instead: inject observability
     via a *test-local* copy of the sequence using a channel/slice shared between a wrapped
     `apply` and a check performed against the actual file's on-disk bytes (statable via `os.File`
     immediately) — pragmatic approach: after `AppendAndApply` returns, assert (a) the segment
     file on disk already contains the record's bytes (proving the fsync completed, since
     Writer.Append doesn't return until Sync() succeeds) and (b) the events slice recorded apply's
     invocation only after a marker appended right after the `w.Append` call. Concretely implement
     via a small package-private test seam: `AppendAndApply` calls `apply()` strictly after
     `w.Append` returns, so the test's `apply` callback appends to a shared slice, and a second,
     independent goroutine-free assertion re-reads the segment file via `ReadSegment` right before
     calling `AppendAndApply` completes is not needed — the simplest and most direct proof: have
     the test's `apply` callback itself call `ReadSegment` on the WAL dir and assert the
     just-written record is already present and CRC-valid *at the moment apply fires* — if fsync
     had not yet happened, `ReadSegment`'s freshly-read bytes could be missing/short. This
     empirically proves fsync-before-apply using only exported API (`ReadSegment`), no fakes
     needed.
   - `TestAppendAndApplyErrorFromApply`: apply returns an error; assert `AppendAndApply` returns
     that error, and that the record IS still durably present on disk (via `ReadSegment`),
     demonstrating the documented "durable log entry survives a failed apply" semantics.
3. Self-consistency: `go build ./engine/...`, `go vet ./engine/...`,
   `go test ./engine/wal/... -race -v -count=1`.
4. Update `.cdr/index/file.jsonl` (new entries for record.go/record_test.go) and
   `.cdr/index/task.jsonl` (`task-1.3.2` -> `implemented`).
5. One local commit, Problem/Solution/Impact style, no push.
