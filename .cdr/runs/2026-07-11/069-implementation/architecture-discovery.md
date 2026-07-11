# Architecture discovery — subtask 4.5.1.5

Read in full before implementing:
- `engine/btree/delete.go`: `Delete`, `repairEmptyLeaf` (single-threaded),
  `shrinkParentAfterMerge`, and the full 2a.4.3/2a.4.5 concurrent section
  (`Tree.Delete`, `crabDelete(Once)`, `deleteFromLeafAndRepair`,
  `repairEmptyLeaf` (Tree method), `repairEmptyLeafAtParent`,
  `finishParentShrinkAfterDelete`, `spliceOutDegenerateAncestor`,
  `findLeftNeighborAtSameLevel`).
- `engine/btree/insert.go`: `insertIntoLeafAndPropagate`'s split branch and
  `Tree.propagate`'s internal-split branch (both contain the "2a.4.5 fix:
  write rightID's own content BEFORE publishing it" doc comment / code).
- `engine/btree/latch.go`: `unlockOrderHook` and
  `TestNodeLatchUnlockOrderingPreventsDoubleLock` (latch_test.go) — the
  idiom this subtask's own tests should mirror (pause a real operation at an
  exact boundary via a package-level hook, then directly probe the
  invariant, rather than a probabilistic stress test).
- `engine/btree/lookup.go`: `optimisticReadHook`/`optimisticRetryHook` and
  `testOptimisticReadForcedRetryDeterministic` (lookup_test.go) — the other
  reference idiom named in the macro.
- `engine/btree/insert.go`: `crabRetryHook` — third existing hook precedent.

## Key facts established

- Both fixes named in the issue ("orphan fix", "publish-last fix") shipped in
  commit `b31328f` (2026-07-06), confirmed unchanged in current source.
- `repairEmptyLeafAtParent` (delete.go) holds a widening 3-latch window
  (parentID, leafID [the emptied leaf], leftID/rightID [chosen sibling]) via
  Lock/TryLock, exactly as documented in its own doc comment.
- The orphan-guard check (`left.NextLeaf != leafID`) sits inside the
  `haveLeftCandidate` branch, immediately after `TryLock(leftID)` +
  `ReadNode(leftID)`, before any borrow/merge decision.
- A NEW bug was found in that exact branch's error-unwind: it unlocks
  `leftID` twice, never unlocks `leafID`. Confirmed via `git log -L` that
  this exact text has been present unchanged since `b31328f` introduced it.
  Confirmed reproducible: manually reverting the fix and running the new
  test triggers `fatal error: sync: unlock of unlocked mutex` deterministically
  every time (not a flaky/racy panic).
- `insertIntoLeafAndPropagate`'s split writes `rightID` (new node) first,
  unlocks it, THEN writes `leafID` (which publishes the pointer via its own
  rewritten `NextLeaf` field), matching the documented "publish-last"
  ordering. Symmetric code exists in `Tree.propagate`'s internal-split
  branch (writes new internal `rightID` first, then `parentID`/`left`).
- No existing hook fires in either of these exact windows (between the two
  split writes, or between the split's completion and `propagate`'s entry).
  Two new test-only hooks were added, following the exact established idiom
  (`nil` in production, invoked synchronously, zero behavior change):
  - `splitPublishHook(newChildID uint64)` — fires right after the new
    child is written+unlocked, strictly before the publish write. Added to
    both the leaf-split site (`insertIntoLeafAndPropagate`) and the
    internal-split site (`Tree.propagate`).
  - `prePropagateHook(oldChildID uint64)` — fires right after the publish
    write completes and all split-local latches are released, strictly
    before `propagate` is (re-)entered. Added at the same two sites.

## Test-construction approach (reusable helpers already in package)

- `newTestStoreAndAllocator(t)`, `writeLeaf`/`writeInternal` (unexported,
  reusable from test files in the same package), `leafEncodedSize`,
  `NodeSize`, `assertAllLookupable`, `assertStructuralInvariants`,
  `assertNoOrphanedPointers` (delete_test.go).
- Rather than building trees via `insertN` + a BFS search for a suitable
  2-leaf-children parent (fragile, indirect), both new tests construct their
  minimal tree directly via fixed node IDs reserved up front with
  `alloc.Next()` (mirrors `TestDeleteSpliceGj0CrossGrandparentNoDangling`'s
  own fixed-ID construction style), and size the target leaf to exactly one
  key below `NodeSize` overflow by growing it in a loop that mirrors the
  production overflow check (`leafEncodedSize(candidate) > NodeSize`) instead
  of a hand-computed magic number. This makes the forced split's trigger key
  and timing fully deterministic.
