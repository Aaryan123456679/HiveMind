# task-2a.2.3 — GC correctness test under concurrent reader/writer/compactor load

## Summary

Closes out subtask 3 of 3 under task-2a.2 / GitHub issue #7 (epoch-based garbage
collection epic), and with it, task-2a.2 and issue #7 in full. Adds
`TestGCUnderConcurrency` (`engine/mvcc/gc_test.go`), a broad concurrency stress
test that runs readers, writers, and the background compactor simultaneously
against the epoch-based GC machinery landed in 2a.2.1/2a.2.2, asserting no
premature reclamation of versions still visible to an open snapshot. This
subtask went through one fix cycle: independent verification found the test's
doc comment overclaimed equivalence to 2a.2.2's deterministic race regression
test, and that readers were only overlapping writers/compactor for ~7% of the
claimed test duration. Both were fixed and independently re-verified.

## Features

- `TestGCUnderConcurrency`: concurrent readers (long-lived open snapshots),
  writers (continuous commits), and the background compactor running for the
  full test duration, asserting the compactor never reclaims a version still
  reachable from an active snapshot.
- Readers now loop on a shared `stop` channel for the entire test duration
  instead of a fixed round count, so the intended reader/writer/compactor
  overlap is actually realized (verified independently at ~100% of wall-clock
  duration, vs. ~7% before the fix).
- Added an active-span assertion that ties the test's pass/fail condition
  directly to the overlap window it claims to exercise, rather than relying on
  incidental timing.
- Corrected doc comment: the test is now accurately described as a
  complementary broad-stress test for general premature-reclaim bugs, not a
  substitute for 2a.2.2's `TestNewSnapshotClosesEpochAcquireVersionReadRace`,
  which remains the only reliable deterministic guard against that specific
  TOCTOU ordering bug.

## Impact

Test-only change; no production code was touched in either the original
implementation or the fix. The fix cycle corrected a documentation overclaim
and a coverage gap in the test itself, not a defect in the GC/epoch machinery
being tested. With this subtask verified, all three subtasks under task-2a.2
(2a.2.1 primitive, 2a.2.2 live wiring, 2a.2.3 concurrency stress test) are now
verified, closing out task-2a.2 and GitHub issue #7 (epoch-based garbage
collection) in full.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run**: `2026-07-05-008-verification`
- Fix cycle: initial verification (`2026-07-05-006-verification`) returned
  CHANGES_REQUESTED — test-only findings only. The doc comment overclaimed
  equivalence to 2a.2.2's deterministic race test (empirically disproven: with
  read.go's pre-2a.2.2-fix ordering reinstated in an isolated copy, 30/30
  `-race` runs of this test passed with zero detections, while the
  deterministic hook-based test failed 1/1 on the same reverted code); and
  readers used a fixed round count finishing in ~107ms of a claimed 1500ms
  duration, leaving ~93% of the test with no open snapshots.
- Fix landed in `2026-07-05-007-implementation-fix` (commit `a1f220d`):
  corrected the doc comment's scope claim, changed readers to loop on a
  shared stop channel for the full test duration, and added an active-span
  assertion.
- Re-verification (`2026-07-05-008-verification`) independently instrumented
  the test and confirmed readers now overlap ~100% of the test duration (vs.
  ~7% before), the new assertion is correctly wired and not fragile, and there
  are zero regressions. Full `-race` suite clean; `TestGCUnderConcurrency`
  clean at 10x and 20x repeat counts.
- Non-blocking comments (edge cases, performance, maintainability): none
  block merge; no further coverage gaps or accuracy issues found.

## Release Notes

Added a concurrency stress test validating that the epoch-based garbage
collector never reclaims a data version while it is still visible to an open
snapshot, under sustained concurrent reads, writes, and background
compaction. Test-only change — no production behavior changes. This
completes the epoch-based garbage collection work (issue #7): the GC
primitive, its live wiring into snapshot/commit paths, and this correctness
test are all now verified.
