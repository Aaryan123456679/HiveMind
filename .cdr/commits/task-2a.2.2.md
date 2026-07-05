# task-2a.2.2 — Snapshot/CommitVersion wiring into epoch-based GC

## Summary

Closes out subtask 2 of 3 under task-2a.2 / GitHub issue #7 (epoch-based garbage
collection epic). Wires the `EpochManager` primitive from 2a.2.2's prerequisite
(2a.2.1) into the live snapshot and commit paths — `NewSnapshot` now acquires the
current epoch, `Snapshot.Close()` releases it, and `CommitVersion` advances the
epoch — and adds the background compactor that reclaims versions no longer
reachable by any live snapshot. This subtask went through one fix cycle: an
independent verification pass found a genuine TOCTOU race in the initial wiring,
which was subsequently fixed and re-verified.

## Features

- `NewSnapshot` acquires the current epoch *before* reading `CurrentVersion`
  (`engine/mvcc/read.go`), establishing a correct happens-before relationship so a
  snapshot always pins an epoch whose referenced version cannot be reclaimed out
  from under it.
- `Snapshot.Close()` releases its acquired epoch via `EpochManager.Release`.
- `CommitVersion` advances the global epoch on each successful commit
  (`commitVersionWithHook`), giving the compactor a monotonically increasing
  watermark to reason about.
- Background compactor (`RunCompaction` / GC path) reclaims versions strictly
  older than `EpochManager.MinReferencedEpoch()` — i.e. versions no live snapshot
  could still be observing.
- Regression test `TestNewSnapshotClosesEpochAcquireVersionReadRace`
  (`engine/mvcc/gc_test.go`) exercises the exact interleaving that previously
  allowed premature reclamation, run 20x under `-race` with no flakes as part of
  re-verification.

## Impact

GC is now live end-to-end: snapshots are correctly epoch-tracked from creation
through close, and the compactor safely reclaims eligible old versions in the
background. This subtask surfaced a real concurrency bug during its first
verification pass — `NewSnapshot` originally read `CurrentVersion` *before*
acquiring the epoch, a TOCTOU race that let a concurrent `CommitVersion`
completing in the window let the compactor prematurely delete a version a live
snapshot still referenced. The fix reordered epoch acquisition ahead of the
version read and closed the race; re-verification independently re-derived the
happens-before proof against the actual mutexes involved (`EpochManager`'s
`em.mu`, the `Catalog`'s per-fileID stripe lock shared by `Get`/`CompareAndSwap`,
and program order in `commitVersionWithHook`) and confirmed the fix is genuinely
race-free with zero regressions elsewhere. The fix does trade a small
GC-effectiveness cost (a slightly wider conservative-retention window before a
version becomes eligible for reclamation) for correctness — flagged as
non-blocking, not a bug. Subtask 2a.2.3 (parent task-2a.2's remaining item) is
still pending.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run**: `2026-07-05-003-verification`
- Fix cycle: initial verification (`2026-07-05-001-verification`) returned
  CHANGES_REQUESTED on the NewSnapshot TOCTOU race described above; the fix
  landed in `2026-07-05-002-implementation-fix` (commits `acc7601`/`afb3fbc`),
  reordering epoch acquisition ahead of the `CurrentVersion` read and adding the
  `TestNewSnapshotClosesEpochAcquireVersionReadRace` regression test.
- Re-verification independently re-derived the happens-before proof against the
  real mutexes in play, confirmed race-freedom, ran the full suite plus the new
  regression test 20x under `-race` with no flakes, and found zero regressions.
- Non-blocking note: the fix's slightly wider conservative-retention window is an
  accepted, deliberate GC-effectiveness/correctness tradeoff, not a defect.

## Release Notes

Wired epoch-based garbage collection into the live snapshot and commit paths: a
background compactor now safely reclaims old versions once no snapshot can still
reference them. Fixed a data-race in the initial wiring (a snapshot could,
under rare concurrent-commit timing, have an old version it was still reading
reclaimed underneath it) before this reached any released build. No other
externally observable behavior changes.
