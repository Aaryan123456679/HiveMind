# Architecture Discovery — 4.5.3.2

## Precedent: engine/btree/latch.go (subtask 4.5.1.3, commit 545e827)

`NodeStore.latches map[uint64]*nodeLatch` was bounded by adding:
- `nodeLatch.refs int`, guarded entirely by `NodeStore.latchesMu` (never atomically).
- `acquireLatch(nodeID)`: lock mu, get-or-create entry, `refs++`, return pointer. Every caller that
  keeps working with the object across a blocking/longer window (Lock, TryLock-success) keeps this
  reference open until a matching `releaseLatch` call.
- `releaseLatch(nodeID, l)`: lock mu, `refs--`; only delete the map entry if `refs == 0 AND
  l.version.Load() == 0` (the gate). The gate exists because evicting a latch that has been
  mutated (`version > 0`) would silently reset a live optimistic-read version counter back to 0 on
  next access, producing a genuine lost-update bug for `Tree.Lookup`'s optimistic read protocol —
  NOT just a memory-bloat concern. Entries that were only locked/probed but never actually
  written (`version == 0`) are the ones safely reclaimed.
- `Unlock` orders "unlock the mutex" BEFORE "release the ref" (which may evict): this is load
  bearing so that any racing `Lock` call — whether it finds the not-yet-evicted object or creates
  a fresh replacement — is always acquiring an *unlocked* mutex, never risking two goroutines with
  simultaneous "ownership" of one nodeID's latch.
- `TryLock` on failure releases its just-acquired ref immediately (never held the mutex, so no
  ordering concern).
- `latchFor`/`getOrCreateLocked` remain the sole NON-refcounted accessor, kept only for
  `WriteNode`'s pre-existing single-threaded test call sites that never pair with a release.

## Current engine/split/guard.go structure (before this change)

`FileGuard.guards map[uint64]*fileSplitState`, guarded by a single `sync.Mutex` (`mu`). Each
`fileSplitState` holds one `atomic.Bool` (`inProgress`) — no refs field, no version-like counter.
`stateFor(fileID)` is the sole accessor: lazily creates on first access, no refcounting at all.
`TryAcquire` = `CompareAndSwap(false, true)` on the entry's bool. `Release` = `Store(false)`
(documented no-op if never acquired — no owner concept, unlike a real mutex). `InProgress` = plain
`Load()`. Doc comment on the struct explicitly says growth is "deliberate, deferred" pending this
revisit.

## Key semantic difference from btree's latch that shapes the gate

btree's node latch carries *history that must survive eviction* (the version counter, needed by
concurrent optimistic readers to detect an intervening mutation). `fileSplitState` carries no such
history: `inProgress` is a pure current-state flag, not a monotonically accumulating counter.
There is nothing for a future re-created entry to "lose" by starting fresh at the zero value
(`inProgress == false`), as long as eviction is correctly gated to only happen when the flag is
*already* false (i.e. no split is currently recorded in progress for that fileID). Therefore:

- The analogous safe-to-evict gate for `FileGuard` is **`refs == 0 AND inProgress.Load() ==
  false`** — simpler than btree's in that there's no "was it ever mutated" history check, but the
  same *shape* of gate (refcount reaches zero AND a state predicate holds).
- The critical invariant to preserve is: an entry must never be evicted while `inProgress == true`
  (a split is recorded in progress for that fileID), because a fresh re-created entry would start
  at `inProgress == false`, letting a *second* concurrent `TryAcquire` for the same fileID
  "win" while the original winner still believes it holds exclusive rights — a genuine double-
  acquisition / mutual-exclusion violation, the FileGuard analogue of btree's lost-update concern.
- Ordering analogous to `Unlock`'s "unlock before release-ref": `Release` must call
  `inProgress.Store(false)` BEFORE decrementing/evicting the ref, not after. If eviction (and thus
  a fresh replacement entry) could happen while the flag is still logically `true` (not yet
  stored false), a racing `TryAcquire` on a freshly-created replacement entry would see
  `inProgress == false` (zero value) and could "win" while the original owner's Release call
  hasn't yet actually cleared the flag — the same double-winner hazard as above, just triggered
  from the release side instead of the eviction-timing side. So the required order is: store
  `false` first, THEN decrement refs / evict-check.
- `Release`'s documented no-op-if-never-acquired contract (existing tests
  `TestReleaseWithoutHoldingIsNoOp`) must be preserved: unlike btree's `Unlock`, which panics if
  called without an outstanding Lock, `FileGuard.Release` has no owner concept and must remain a
  safe no-op on an absent entry — so `Release` uses a plain map lookup (not `peekLatch`'s
  panic-on-miss behavior) and simply returns if the entry doesn't exist.

## Files read
- `/Users/aaryanmahajan/Main/Projects/HiveMind/engine/btree/latch.go` (full file, 318 lines).
- `/Users/aaryanmahajan/Main/Projects/HiveMind/engine/split/guard.go` (full file, 142 lines).
- `/Users/aaryanmahajan/Main/Projects/HiveMind/engine/split/guard_test.go` (full file, 154 lines,
  existing tests: TestSplitInProgressCAS, TestAcquireReleaseReacquire, TestIndependentFileIDs,
  TestReleaseWithoutHoldingIsNoOp, TestInProgressObservability — all must keep passing unchanged).
- `.cdr/memory/pending.md` (FileGuard eviction deferred-limitation note, line 19).
