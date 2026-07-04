# Plan — task-2a.2.1

## Epoch model (foundational — read this before touching 2a.2.2/2a.2.3)

- **Global**, not per-fileID, monotonically increasing `uint64` counter.
  - Numerically: epoch N means "the state of the world after the Nth call to
    `AdvanceEpoch`". Epoch 0 is reserved as a sentinel ("never acquired anything" / zero
    value), so the counter starts at 1 and `AdvanceEpoch` is expected to be called once
    up front (or lazily on first acquire) before any real acquisition happens. This
    subtask's `EpochManager` starts `current` at 1 so `AcquireCurrentEpoch` always
    returns a valid (>=1) epoch even before any `AdvanceEpoch` call.
  - Future wiring (2a.2.2): each successful `CommitVersion` call bumps the global epoch
    via `AdvanceEpoch()` exactly once per successful CAS (not per attempt/retry), so
    epoch boundaries line up with "a new version became visible as CurrentVersion for
    SOME fileID in the whole store" — the coarsest-but-simplest visibility boundary that
    still lets `MinReferencedEpoch` answer "is it safe to reclaim a version that was
    superseded before epoch E" for any fileID.
  - Future wiring: each `Snapshot`, at `NewSnapshot` time, calls
    `EpochManager.AcquireCurrentEpoch()`, which increments that epoch's refcount and
    returns which epoch number was acquired (stored on the Snapshot). A future
    `Snapshot.Close()` calls `EpochManager.Release(epoch)`.
  - GC (2a.2.2/2a.2.3): a version superseded strictly before `MinReferencedEpoch()` (or
    when there are no live snapshots at all) is safe to reclaim, since no live snapshot
    could have captured `CurrentVersion` while an older version was still current at or
    after that epoch.

- Why global over per-fileID: the requirement text explicitly floats both options and
  says to pick the simplest one that supports 2a.2.2/2a.2.3's stated need ("old versions
  no longer referenced by ANY live snapshot"). That phrasing is inherently
  store-wide/global, not scoped to one fileID, so a single global counter is both
  simpler and sufficient. A per-fileID scheme would need a separate counter + refcount
  map per fileID for no additional correctness benefit at this stage.

## API surface (engine/mvcc/gc.go)

```go
type EpochManager struct {
    mu       sync.Mutex
    current  uint64
    refcounts map[uint64]int64
}

func NewEpochManager() *EpochManager
func (em *EpochManager) CurrentEpoch() uint64
func (em *EpochManager) AdvanceEpoch() uint64
func (em *EpochManager) AcquireCurrentEpoch() uint64   // increments refcount of CurrentEpoch(), returns it
func (em *EpochManager) Release(epoch uint64) error    // decrements; errors (does not panic) if it would go negative
func (em *EpochManager) RefCount(epoch uint64) int64   // 0 for never-acquired/zeroed-out epochs
func (em *EpochManager) MinReferencedEpoch() (epoch uint64, ok bool) // smallest epoch with refcount > 0; ok=false if none
```

- Thread safety: single mutex guarding `current` + `refcounts` map. A plain map (not
  `sync.Map`) is fine and simpler here: unlike `VersionWriter`'s per-fileID sharding
  (many independent fileIDs, contention matters), epoch operations are store-wide and
  inherently need a linearizable view across `current`/`refcounts` together (e.g.
  `AcquireCurrentEpoch` must read `current` and bump `refcounts[current]` atomically
  as one unit) — a single mutex is the correct and simplest tool, not a missed
  optimization opportunity.
- `Release` returns an error (not panic) on double-release / over-release, since this is
  a library-level API that ordinary buggy caller code could trigger; propagating an
  error is more idiomatic Go and testable than a panic, and avoids crashing an entire
  process due to one bad Close() call elsewhere. Documented explicitly in the doc
  comment.
- `refcounts` entries are pruned (delete from map) once they hit 0, both to keep
  `MinReferencedEpoch` cheap (no need to skip zeroed entries indefinitely) and so
  `RefCount` naturally returns 0 for both "never acquired" and "acquired then fully
  released" epochs.

## Decision recap (see architecture-discovery.md)

- `EpochManager` is a NEW, standalone type — `Snapshot` (read.go) is NOT modified in
  this subtask. No existing test call site changes. This is intentionally scoped:
  2a.2.1's acceptance criteria is about the refcounting primitive; wiring it into
  `NewSnapshot`/`CommitVersion` end-to-end is 2a.2.2/2a.2.3's job once there's an actual
  GC consumer for `MinReferencedEpoch`.

## Test plan (gc_test.go)

`TestEpochRefcount`:
1. Fresh `EpochManager`; assert `CurrentEpoch() == 1`, `RefCount(1) == 0`.
2. Acquire epoch 1 three times (simulating 3 overlapping snapshots at epoch 1); assert
   `RefCount(1) == 3` after each acquire (checked incrementally, not just at the end).
3. `AdvanceEpoch()` -> epoch becomes 2; assert `CurrentEpoch() == 2`; acquire epoch 2
   twice; assert `RefCount(2) == 2`, `RefCount(1)` still 3 (unaffected by advance).
4. Release epoch-1 refs out of order (not LIFO: release 2nd-acquired, then 1st, then
   3rd); assert refcount decrements correctly at each step, hits exactly 0 after the
   3rd release, and `RefCount(1) == 0` afterward.
5. Assert `MinReferencedEpoch()` reflects the smallest epoch with refcount > 0 at each
   stage (e.g. epoch 1 while any of its 3 refs are alive; epoch 2 once epoch 1 fully
   drains; `ok == false` once everything is released).
6. Deliberately double-release: call `Release(1)` an extra time after it's already at 0
   and assert this returns a non-nil error (not a panic, not silent negative
   corruption), and that `RefCount(1)` remains 0 (not negative) afterward.
7. Run with `-race` and multiple goroutines acquiring/releasing concurrently across 2-3
   epochs to exercise the mutex under `go test -race`.
