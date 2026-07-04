# task-1.3.5 — Crash-injection recovery test (mid-write crash simulation)

## Summary
Closes out the final checklist item of GitHub issue #3 (Phase 1 Storage
core / WAL crash-safety). Adds the crash-injection recovery test required
by the issue's literal acceptance criteria, proving that the WAL detects
and discards a torn (partially-written) tail record left behind by a
mid-write crash and resumes recovery cleanly from the last valid record,
with no panic and no data corruption. Also closes four previously-deferred
gaps flagged by earlier verifications of 1.3.1, 1.3.2, and 1.3.4.

## Features
- Crash-injection recovery test covering both torn-payload and
  torn-header tail scenarios on the actively-written segment.
- Hardened `OpenWriter` resume path: on restart, a torn tail is now
  discarded and the segment truncated (rather than left unvalidated)
  before the writer reopens for append.
- Real subprocess-level crash simulation (self re-exec + kill) proving
  no in-process/Go-level buffering hides an unfsynced write.
- CRC-corruption-during-replay coverage, plus a bug fix so `Replay`
  applies all prior valid records before surfacing a hard error instead
  of discarding already-parsed valid work.
- New `Writer.Offset()` accessor for future checkpoint-pointer callers.
- Explicit (stricter-than-required) hard-error policy for a torn tail
  found in a non-last segment, since only the actively-written segment
  can legitimately be torn after a real crash.

## Impact
The WAL subsystem's crash-safety contract is now fully implemented and
verified end-to-end: append, fsync-before-apply, checkpointing, ordered
replay, and crash recovery all have dedicated, passing test coverage.
This closes all 5 subtasks (1.3.1-1.3.5) of GitHub issue #3. Known
non-blocking follow-ups: `docs/LLD/wal.md` remains stale relative to the
as-built torn/CRC semantics and should be synced in a future subtask;
there is no dedicated concurrent-`Offset()` race test (low risk, mirrors
the pre-existing `SegmentNum()` pattern).

## Verification
- Verdict: PASS_WITH_COMMENTS
- Run: 2026-07-04-044-verification

## Release Notes
WAL crash recovery has been hardened: a partially-written record left by
a process crash mid-write is now reliably detected and discarded on
restart, with all prior durable records replayed intact.
