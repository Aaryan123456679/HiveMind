# task-2a.2.1 — Snapshot epoch refcounting

## Summary

Closes out subtask 1 of 3 under task-2a.2 / GitHub issue #7 (epoch-based garbage
collection epic) by adding a new, standalone `EpochManager` primitive
(`engine/mvcc/gc.go`) that tracks a global, monotonically increasing epoch counter
together with per-epoch reference counts. This is the foundational refcounting
mechanism that later subtasks (2a.2.2, 2a.2.3) will wire into `Snapshot` creation/close
and `CommitVersion` to make GC-safe reclamation decisions.

## Features

- `EpochManager` (`engine/mvcc/gc.go`): a single-mutex-guarded type exposing
  `CurrentEpoch()`, `AdvanceEpoch()`, `AcquireCurrentEpoch()` (increments the current
  epoch's refcount and returns which epoch was acquired), `Release(epoch)` (decrements,
  returning an error rather than panicking on double-release/over-release), `RefCount(epoch)`,
  and `MinReferencedEpoch()` (the smallest epoch with a live refcount — the conservative
  watermark below which no live snapshot could still be observing an older version).
- Refcounting is structurally guaranteed non-negative: entries are pruned from the
  internal map as soon as they hit zero, so `RefCount` naturally reads 0 for both
  "never acquired" and "fully released" epochs, and `Release` on an unacquired/already-
  zeroed epoch is rejected as an error instead of corrupting state.
- Global (not per-fileID) epoch counter, matching the store-wide visibility boundary
  needed by GC ("no longer referenced by ANY live snapshot") — deliberately the simplest
  model that satisfies that requirement, per the pre-implementation plan.
- Test coverage: `TestEpochRefcount` and `TestEpochRefcountConcurrent` in
  `engine/mvcc/gc_test.go`, including a genuine double-release error case and repeated
  `-race -count=10` runs with no flakes.

## Impact

Adds a correct, independently testable refcounting primitive that GC will depend on.
**`EpochManager` is a brand-new, standalone type and is NOT YET wired into `Snapshot` or
`CommitVersion`** — `NewSnapshot` does not yet call `AcquireCurrentEpoch`, there is no
`Snapshot.Close()`/`Release` call site, and `CommitVersion` does not yet call
`AdvanceEpoch`. No existing read/write/commit code paths are touched by this subtask,
so GC is not live yet and no snapshot in the current system is actually being
epoch-tracked. Wiring this primitive into the live snapshot/commit paths is explicitly
2a.2.2's job; that subtask's verification must explicitly confirm the wiring lands
before GC correctness can be assumed end-to-end.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run**: `2026-07-04-082-verification`
- Confirmed: refcounting structurally cannot go negative; `MinReferencedEpoch`
  semantics hand-verified as a correct conservative watermark; deferring
  Snapshot/CommitVersion wiring to 2a.2.2 is the original intended scope (corroborated
  by the pre-implementation planner decomposition), not a post-hoc narrowing of this
  subtask's acceptance criteria.
- Non-blocking nit: consider renaming `AcquireCurrentEpoch` to make its refcount side
  effect more discoverable from the name alone.
- Flagged residual risk (carried forward to 2a.2.2): its verification must explicitly
  confirm that `NewSnapshot` calls `AcquireCurrentEpoch` and that a new
  `Snapshot.Close()` calls `Release`.

## Release Notes

Added the epoch refcounting primitive (`EpochManager`) that will underpin upcoming
epoch-based garbage collection. This is an internal, not-yet-wired building block — no
observable behavior change yet; GC and snapshot lifecycle are unaffected until 2a.2.2
lands.
