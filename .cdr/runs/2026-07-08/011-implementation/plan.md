# Plan

1. `engine/wal/writer.go`: add `WriteSegmentFloor(dir, floor)` (atomic temp+fsync+rename,
   monotonic - never lowers an existing floor) and a `readSegmentFloor` helper; teach
   `latestSegmentNum` to consult the floor file only when a directory currently has zero
   `wal-*.log` segment files, and to ignore it entirely as soon as any real segment file
   exists.
2. `engine/graph/edgelog.go`: `TruncateNode` no longer deletes the node's log directory.
   Before removing any segment file, compute the highest segment number currently on
   disk (via existing `listWALSegmentsNumbered`) and call `wal.WriteSegmentFloor(dir,
   maxSeg+1)`, THEN remove the segment files. Remove the now-dead `listWALSegments`
   helper (superseded, no remaining callers).
3. `engine/graph/compact.go`: update the package doc comment with a new "Segment-number
   reuse (second fix cycle)" section, and update `edgeLogNodeIDs`'s comment to reflect
   that node directories now persist after truncation.
4. `engine/graph/compact_test.go`: add
   `TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost` (F2's exact scenario,
   run 3x to confirm segment numbering keeps advancing) and
   `TestCompaction_FailedTruncateRetryThenOrdinarySubsequentAppendsSurvive` (F1's
   failed-retry scenario immediately followed by F2's ordinary-append scenario on the
   same node).
5. Confirm both new tests FAIL against pre-fix commit 9850083 (worktree revert-experiment)
   and PASS against the fix.
6. gofmt / go vet / go build ./... clean; `-race` on graph and wal packages; full module
   suite `go test ./... -count=1 -timeout 25m`.
