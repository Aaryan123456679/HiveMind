# Requirement — Subtask 1.3.3

Source: `gh issue view 3` (Epic "Phase 1: Storage core"), checklist item 1.3.3, verbatim.

## Acceptance criteria
Checkpointing records the WAL offset up to which state is durably applied; segments fully
before the checkpoint pointer are eligible for truncation/archival.

## Test spec
`go test ./engine/wal/... -run TestCheckpointManifest`: write records, checkpoint, assert
manifest.json reflects correct offset and old segments are archivable.

## Impacted modules
`engine/wal/checkpoint.go`, `engine/wal/checkpoint_test.go`

## Context from prior subtasks (verified, not to be modified)
- 1.3.1 (`task-1.3.1`, commit `1a12643c`): `Writer`/`OpenWriter`/segment rotation, `wal-<N>.log`
  naming, `ReadSegment`. `Writer.Append` returns a **per-segment** byte offset (the offset within
  the current segment file at the time of write, reset to 0 on rotation), not a global monotonic
  offset. `Writer.SegmentNum() int` returns the segment currently being written.
- 1.3.2 (`task-1.3.2`, commit `4e418a1`): `TypedRecord`/`RecordType`, `AppendAndApply`
  (fsync-before-apply).
- Regression note (038-verification, low risk, not this subtask's job): `DecodeTypedRecord` does
  not validate `RecordTypeInvalid`/out-of-range `RecordType` bytes — relevant context for 1.3.4's
  recovery dispatch, out of scope here.
- `docs/LLD/wal.md` is flagged stale/scaffold-only across both prior verifications; a sync pass
  was recommended but is not blocking for 1.3.3. Noted again here, not addressed in this run.

## Design guidance supplied by task issuer
- Checkpoint pointer representation: `{SegmentNumber, OffsetInSegment}` tuple, consistent with
  `Writer.Append`'s per-segment offset semantics (confirmed by reading writer.go, see
  architecture-discovery.md).
- `manifest.json`: small `encoding/json` file, NOT part of the binary on-disk record format;
  written atomically via temp-file + `Sync()` + `os.Rename`.
- API: `Checkpoint(dir string, segmentNumber uint64, offsetInSegment int64) error`,
  `LoadCheckpoint(dir string) (segmentNumber uint64, offsetInSegment int64, found bool, err error)`,
  `ArchivableSegments(dir string, checkpointSegmentNumber uint64) ([]string, error)`.
- `ArchivableSegments` only *identifies* eligible segments (strictly before the checkpoint's own
  segment number); it does not delete/archive them — that is out of scope for 1.3.3.
