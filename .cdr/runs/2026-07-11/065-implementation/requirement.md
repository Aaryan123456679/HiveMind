# Requirement — Subtask 4.5.4.5 (Issue #41)

Update `docs/LLD/wal.md` from scaffold-level to a full LLD document accurately
describing the current state of `engine/wal/*.go`. This is a docs-only change;
no production code is touched.

## Acceptance criteria

`docs/LLD/wal.md` must document:

1. The concrete 8-byte record header format: `[0:4]` uint32 LE payload
   length, `[4:8]` uint32 LE CRC32 (IEEE) checksum of the payload
   (`recordHeaderSize` in `writer.go`).
2. `wal-<N>.log` segment naming/rotation: plain (non-zero-padded) base-10
   segment numbers starting at 0, size-based rotation via
   `maxSegmentBytes`, rotate-before-write (never split a record across
   segments), segment-floor mechanism (`WriteSegmentFloor`/`.segment-floor`)
   for callers that truncate/remove segments out from under the writer.
3. The `RecordType` vocabulary: `RecordTypeInvalid` (0, reserved/never
   valid), `RecordCatalogPut` (1), `RecordCatalogDelete` (2),
   `RecordBTreeInsert` (3), `RecordBTreeDelete` (4), `RecordSplitCommit`
   (5) — including each payload's encoding — and the
   `RecordTypeInvalid`/out-of-range validation guard added in subtask
   4.5.4.3 (commit `4c60202`, `DecodeTypedRecord` rejects type 0 and any
   type > `RecordSplitCommit`).
4. The checkpoint `manifest.json` schema (`CheckpointPointer`:
   `segment_number` uint64, `offset_in_segment` int64), its atomic
   temp-file+Sync+rename write path, and `ArchivableSegments`' boundary
   semantics (segments strictly less than the checkpoint's segment number
   are archivable; the checkpoint's own segment and anything newer are
   not) — including the corrected atomic-write doc-comment lineage from
   subtask 4.5.4.4 (commit `ab5e962`): the temp-file+Sync+rename idiom is
   new to this codebase, NOT modeled on `engine/btree/persist.go`'s
   `SaveRoot` (which uses a weaker in-place `WriteAt`+`Sync`, no temp file,
   no rename).
5. `Replay`'s contract: `LoadCheckpoint` fallback to (segment 0, offset 0)
   when no manifest exists; `OffsetInSegment`'s inclusive-start convention
   (replay resumes AT that offset, i.e. everything before it is skipped as
   already-applied, everything from it onward including a record starting
   exactly there is replayed); the `RecordType` validation gate
   (`isValidRecordType`) that hard-errors on invalid/unrecognized types
   during replay.
6. Torn-tail-validation/crash-recovery discipline landed in task-1.3.5:
   the torn-tail-vs-CRC-corruption distinction in `parseSegmentRecords`
   (truncated header/payload at EOF = torn tail, cleanly stops parsing,
   not an error; CRC mismatch on a full-length record = hard error, never
   silently discarded); `OpenWriter`'s `repairTornTail` physically
   truncating a resumed segment's torn tail before reopening for append;
   `Replay`'s rule that a torn tail is only legitimate in the last
   (highest-numbered) segment — a torn tail in an earlier segment is a
   hard on-disk-inconsistency error.

## Test spec

Doc-only change. Manual review against `engine/wal/*.go` for accuracy.
No automated test is required; optionally `go test ./engine/wal/... -race`
as a sanity check that the package itself is unaffected (no source files
are touched).

## Scope

Impacted file: `docs/LLD/wal.md` only. Do not touch any other module
(other agents are concurrently working on engine/btree, engine/split,
engine/catalog in this checkout).
