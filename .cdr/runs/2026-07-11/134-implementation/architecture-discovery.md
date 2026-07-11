# Architecture Discovery

## Token order followed
1. `.cdr/index/file.jsonl`, `.cdr/index/regression.jsonl`, `.cdr/index/task.jsonl` (grepped for engine/wal, 4.5.14, rollover/segment).
2. `docs/LLD/wal.md` (Storage layout / rotation / OpenWriter resume semantics).
3. Targeted source read: `engine/wal/writer.go` (full file) and `engine/wal/writer_test.go` (existing tests, full file for conventions).

## Key facts gathered

- `docs/LLD/wal.md` "Record header & rotation" section: segments are
  size-rotated; `OpenWriter` rotates to `wal-<segmentNum+1>.log` **before**
  writing if the incoming record would overflow the current segment — a
  record is never split/partially written across two segments.
- `writer.go` `Append` (lines 335-373): rotate condition is
  `w.size > 0 && w.size+total > w.maxSegmentBytes` — a **strict** `>`, so a
  record whose `w.size+total` lands exactly equal to `maxSegmentBytes` does
  NOT rotate; it stays in the current (now-exactly-full) segment. This is the
  precise boundary behavior the new test must pin down.
- `writer.go` `latestSegmentNum` (lines 198-239): scans dir for
  `wal-<N>.log` files, returns the highest `N` found with `resuming=true`.
  This is what "correct segment selection" on resume means: with >1
  pre-existing segment file, `OpenWriter` must select the highest-numbered
  one, not segment 0 and not a fresh one.
- `writer.go` `OpenWriter` (lines 94-157): when resuming, restores `size` from
  `repairTornTail`'s `validSize` (here, simply the resumed segment's true
  on-disk size, since no torn tail is injected in this test) and reopens with
  `O_APPEND`.
- Existing `writer_test.go` conventions (read in full): `listSegmentFiles`
  helper, `segmentPath(dir, n)` package-internal helper, table/three-step
  style (`OpenWriter` -> `Append`s -> assert via `SegmentNum()`/`Offset()`/
  `ReadSegment`). `TestOpenWriterResumesExistingSegments` is the closest
  existing analog but only exercises a single pre-existing segment — exactly
  the gap this subtask closes.
- `.cdr/index/regression.jsonl` (run 036-verification, subtask 1.3.1) is the
  authoritative source of the gap this subtask closes (see requirement.md).
- `.cdr/index/regression.jsonl` (subtask 4.5.14.1, commit 9774bee) confirms
  the sibling doc-only subtask landed and touched only `writer.go`, leaving
  `writer_test.go` untouched — clean base confirmed.

## Files touched by this subtask
- `engine/wal/writer_test.go` (new test function only, additive).

No other file needs to change; `writer.go`'s rotate/resume logic is already
correct by inspection (per 036-verification's own note: "correctly kept in the
current segment per code trace") — this subtask only adds the missing test,
per the issue's literal scope.
