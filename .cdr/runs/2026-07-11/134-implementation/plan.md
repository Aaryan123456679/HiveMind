# Plan

1. Add `TestExactBoundarySegmentRolloverResume` to `engine/wal/writer_test.go`,
   placed after `TestOpenWriterResumesExistingSegments` (its closest existing
   analog) and before `TestOpenWriterResumeTornTailDiscardsAndTruncates`.

2. Test structure:
   a. **Exact-boundary sub-scenario**: `maxSegmentBytes = 64`. Append two
      24-byte payloads (each header(8)+payload(24) = 32 bytes total), so
      after both, `w.size == 64 == maxSegmentBytes` exactly. Assert
      `SegmentNum() == 0` and `Offset() == maxSegmentBytes` (no premature
      rotation on an exact-equal total, per `Append`'s strict `>` rotate
      condition).
   b. Append a third record: since segment 0 is already exactly full, this
      must rotate into segment 1. Assert `SegmentNum() == 1` after this
      append. Close the writer.
   c. Assert on disk: segment 0's file size is exactly `maxSegmentBytes`, and
      `ReadSegment` on it returns exactly `[record1, record2]` (the boundary
      record did not spill/split into segment 1).
   d. Assert exactly 2 pre-existing segment files exist on disk before resume
      (the literal ">1 pre-existing segment" precondition from the issue).
   e. **Resume sub-scenario**: call `OpenWriter` again against the same dir.
      Assert it selects segment 1 (`SegmentNum() == 1`, not 0, not a fresh
      segment 2) and restores `Offset()` to segment 1's true pre-existing
      size (`recordHeaderSize + len(record3)`).
   f. Append a fourth record via the resumed writer; assert it lands in
      segment 1 (no spurious rotation, since it fits comfortably).
   g. Final assertions: segment 0 unchanged (`[record1, record2]`); segment 1
      now contains exactly `[record3, record4]` in order; exactly 2 segment
      files exist on disk afterward (no stray third segment created).

3. Run `go test ./engine/wal/... -run TestExactBoundarySegmentRolloverResume -v`
   to confirm the new test passes in isolation.

4. Run `go vet ./engine/wal/...`, `gofmt -l engine/wal/`, and
   `go test ./engine/wal/... -race` to confirm no regressions across the full
   package test suite (self-consistency, not verification).

5. Stage only `engine/wal/writer_test.go` plus this run's own
   `.cdr/runs/2026-07-11/134-implementation/` artifacts. Confirm via
   `git diff --cached --stat` that no other file is staged before committing.

6. Single local commit (no push), using the user's commit-message standard
   (type: summary / Problem / Solution / Impact).

7. Write `handoff.json` with pointers only (no pasted source) for the
   downstream `/cdr:verify` agent.
