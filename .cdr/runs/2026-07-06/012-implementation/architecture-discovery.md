# Architecture discovery — task-2a.4.5

Read in full: `.cdr/commits/task-2a.4.1.md`..`task-2a.4.4.md`, current
`engine/btree/{latch.go,lookup.go,insert.go,delete.go}`, and the existing
test files' helper/oracle patterns (`insert_test.go`, `delete_test.go`,
`lookup_test.go`, `btree_test.go`).

## Key entry points (same package `btree`, all reusable from a new/appended
`btree_test.go` file without any export changes)
- `Tree` struct (`insert.go:507`): `Store *NodeStore`, `Alloc *NodeAllocator`,
  `rootMu sync.Mutex`, `root uint64`. `NewTree(store, alloc, rootNodeID) *Tree`.
  `t.Root() uint64` — safe concurrent read of current root.
- `t.Insert(path string, fileID uint64) error` (`insert.go:541`) — latch-
  crabbing, deadlock-free-by-construction (TryLock + full release + restart-
  from-root, `errRestartFromRoot`/`crabRetryBackoff`/`crabRetryHook`).
- `t.Delete(path string) (found bool, err error)` (`delete.go:444`) — same
  TryLock/restart discipline, reused from insert.
- `t.Lookup(path string) (fileID uint64, found bool, err error)` (`lookup.go:356`)
  — lock-free, optimistic, version-bracketed (`readNodeOptimistic`,
  `errOptimisticRetry`, `optimisticReadHook`/`optimisticRetryHook` test hooks).
  Never calls `Lock`/`TryLock` on a per-node latch (only `t.Root()` briefly
  takes `rootMu` — tracked non-blocking follow-up F1 in pending.md).
- Free (Phase-1, single-threaded) `Lookup(store, rootNodeID, path)` — untouched,
  independent implementation; capstone cross-checks it against `Tree.Lookup`.

## Reusable test helpers already in package (no re-invention needed)
- `newTestStoreAndAllocator(t) (*NodeStore, *NodeAllocator)` (insert_test.go)
- `genKey(i int) string` — `"topic%04d/page"` (insert_test.go)
- `insertN(t, store, alloc, n) (rootID uint64, inserted map[string]uint64)`
  (insert_test.go) — serial seeding via real `Insert` path.
- `assertAllLookupable(t, store, rootID, wantPresent map[string]uint64)` (insert_test.go)
- `assertAbsent(t, store, rootID, keys []string)` (delete_test.go)
- `assertStructuralInvariants(t, store, rootID, wantKeyCount int)` (insert_test.go)
  — sorted keys/fanout/NextLeaf global order/NextSibling chain per level/LowKey.
- `assertNoOrphanedPointers(t, store, rootID)` (delete_test.go)
- `crabRetryHook` (insert.go), `optimisticReadHook`/`optimisticRetryHook`
  (lookup.go) — package-level test-only synchronization hook vars, already
  used by `TestCrabbingConcurrentPropagateNoDeadlock` and
  `TestOptimisticRead/ForcedRetryDeterministic` for deterministic forced-
  interleaving tests. Save/restore via `t.Cleanup`.

## Existing oracle-partitioning precedent to mirror/extend
- `testCrabbingDeleteInterleavedWithInsert` (delete_test.go:647): disjoint-by-
  index-mod-3 key ownership between delete goroutines and insert goroutines,
  final-state oracle computed from the same partitioning, `n=4000`,
  16+16 goroutines.
- `testOptimisticReadInterleavedWithInsertDelete` (lookup_test.go:249):
  3-way index partition (deleted / inserted-new / untouched) with continuous
  concurrent `Tree.Lookup` goroutines throughout, oracle checked per-key as
  "one of the possible point-in-time answers", untouched keys required
  found with exact original fileID on every single lookup.
- `testCrabbingInsertVeryDeepOverlappingSubtree` (insert_test.go:715): 160
  goroutines / 80,000 keys disjoint-by-modulo insert-only stress, this
  package's largest scale to date; skipped in `-short` mode.
- `TestCrabbingConcurrentPropagateNoDeadlock` /
  `TestOptimisticRead/ForcedRetryDeterministic`: hook-based deterministic
  forced-interleaving pattern (pause goroutine mid-op via a package-level
  hook var, mutate the exact node from another goroutine, release, assert).

## Findings relevant to this subtask's design
- `btree_test.go` already exists (task-1.2.6); must APPEND, not overwrite.
- fileID-space collision avoidance is the trick that has made every prior
  oracle in this package unambiguous despite concurrent scheduling: this
  subtask uses a per-key-index-derived, globally-unique fileID encoding
  (`fileID = index*10 + version`) so a corrupted/cross-key result is
  detectable by construction (any observed fileID not in that key's own
  precomputed valid-set is definitionally corruption), rather than needing
  runtime cross-checking against other goroutines' state.
- `assertStructuralInvariants` requires an exact final leaf-level key count
  (`wantKeyCount`) — the oracle's final `wantPresent` count must be exact,
  which is straightforward since every operation's final effect is fixed by
  static partitioning, not by which goroutine "wins" a race.
