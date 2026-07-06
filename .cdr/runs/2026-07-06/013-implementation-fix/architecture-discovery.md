# Architecture discovery

Read in full (per task instructions): `.cdr/commits/task-2a.4.1.md` through
`task-2a.4.4.md`, `engine/btree/insert.go` (`Tree.Insert`, `crabInsertOnce`,
`findParent`/`findParentOnce`, `propagate`), `engine/btree/delete.go`
(`Tree.Delete`, `crabDeleteOnce`, `repairEmptyLeafAtParent`,
`finishParentShrinkAfterDelete`, `spliceOutDegenerateAncestor`,
`findLeftNeighborAtSameLevel`), `engine/btree/latch.go`, and the
uncommitted `engine/btree/btree_test.go` (`TestConcurrentMixedWorkload`,
`TestConcurrentMixedWorkloadForcedLookupDuringDelete`).

Key established conventions confirmed still intact and NOT the defect:
- `NodeStore.Lock`/`TryLock`/`Unlock` are a genuine per-node-ID mutex
  (`latch.go`); insert.go and delete.go both funnel every mutation of a
  node's content through this same latch -- no latching-scope mismatch
  between the two files' parent-mutation paths.
- `propagate`'s positional-index (`pos := j`) logic and its permanent
  bounds-check invariant guards are correct in isolation; `j` is
  recomputed fresh under `parentID`'s lock every time, so there is no
  TOCTOU on the parent's own Children/Keys between read and write.
- `InternalNode.LowKey`-based "move right" peeking at the internal-node
  level (used by `crabInsertOnce`/`crabDeleteOnce`/`findParentOnce` when
  walking `NextSibling`) is correct: it never trusts a sibling's own
  currently-populated Keys, exactly to avoid the ambiguity described below.

Empirically confirmed root cause (via a temporary, non-committed 20-goroutine
minimal repro instrumented with a full tree-dump + per-write trace hook, run
dozens of times): the LEAF-level "move right" peek in `crabInsertOnce`
(insert.go), `crabDeleteOnce` (delete.go), and `Tree.Lookup`'s optimistic read
(lookup.go) all shared the same flawed condition:

    if len(nextLeaf.Keys) > 0 && path < nextLeaf.Keys[0] { stay } else { move right }

This is correct for 2a.4.2 (insert-only): a `NextLeaf` sibling was always the
just-created right half of a split, which by construction always has >= 1
key, so "sibling is empty" was structurally impossible and the implicit
"move right whenever empty" fallthrough was unreachable dead code, not a
deliberate decision.

2a.4.3's Delete introduces exactly that previously-impossible case: its
tombstone policy leaves a fully-drained leaf (0 keys) linked in the
`NextLeaf` chain until its own `repairEmptyLeafAtParent` borrow/merge
completes. Such a leaf carries no usable lower-bound key of its own. Any
concurrent crabbing walk (Insert, Delete, or Lookup) sitting at that empty
leaf's LEFT neighbor, upon peeking it and finding it empty, incorrectly
falls through to "move right" -- even though the parent-level routing
decision (`sort.Search` over the parent's own Keys) had *already* correctly
selected the left neighbor as the target. The walk then writes (or, for
Lookup, reads) at the wrong, unrelated, out-of-range leaf. Because that
misrouted write never overflows NodeSize, it never triggers a split/
`propagate` call, so it silently bypasses the parent-separator-update
machinery entirely -- corrupting the leaf-chain's sortedness invariant.
Confirmed via trace: in the repro, `writeInternal` on the shared parent node
was called exactly once (the original preseed split) for the entire run,
while dozens of erroneous leaf-2 writes (containing wrong-range,
insert-only keys) occurred afterward with no corresponding parent update --
proving the corruption path bypasses `propagate` altogether, exactly as the
mechanism above predicts.

This single root cause explains both observed symptoms: silent data loss
(confirmed directly, reproduced dozens of times) and, in other
interleavings, the `propagate` invariant panic (a subsequent legitimate
split whose promoted key gets compared against a parent separator that no
longer matches the leaf's actual, corrupted content).

## Two further root causes found during full-scale (`TestConcurrentMixedWorkload`, `-race`) validation

The 20-goroutine minimal repro above is necessary but not sufficient: it
exercises only leaf-level insert/delete on disjoint ranges, with no leaf
merges/borrows crossing an intervening concurrent split, and no lookups
racing a fresh split. Running the fixed code against the full capstone test
(200 goroutines, 80k keys, mixed insert/delete/lookup) surfaced two
additional, independent, genuine bugs, proven the same way (empirical
instrumented repro, not theorized):

### Root cause 2: `repairEmptyLeafAtParent`'s borrow/merge-from-left
    branch can orphan a concurrently-split-but-not-yet-propagated node

`repairEmptyLeafAtParent` (delete.go) holds `parentID`'s latch for its
entire borrow-or-merge decision. Its borrow-from-left and merge-into-left
branches both unconditionally overwrite `leftID`'s `NextLeaf` field
(`newLeft.NextLeaf = leafID` / `mergedLeft.NextLeaf = emptyNextLeaf`),
implicitly assuming `leftID`'s CURRENT `NextLeaf` still equals `leafID`
(i.e. they are still true, immediate chain neighbors).

That assumption can be violated: a concurrent `Insert` can split `leftID`
(writing the new right-half node, e.g. node13, and updating `leftID`'s own
`NextLeaf` to point at it) entirely under `leftID`'s own latch, WITHOUT
needing `parentID`'s latch at all -- `propagate` (which needs `parentID` to
link node13 into `parentID`'s Children) is only called AFTER the split's
node writes are done and `leftID`'s latch has already been released. If
`repairEmptyLeafAtParent` is already holding `parentID` when this happens,
the split's own `propagate` call necessarily blocks (TryLock-restart loop)
waiting for `parentID`, while `repairEmptyLeafAtParent` proceeds to
`TryLock(leftID)` and reads its already-split, fresh content
(`leftID.NextLeaf == node13`, not `leafID`). Blindly overwriting
`leftID.NextLeaf` with `leafID` or `emptyNextLeaf` at that point discards the
live link to node13, permanently orphaning it (and any further descendants)
even though node13 is still correctly listed in a pending, about-to-land
`propagate` call and holds live, correctly-ordered data.

Confirmed via a reduced-scale (10+10 goroutines, 1000+1000 keys) repro with
a full tree-dump on failure: orphaned leaf(s) (e.g. node 13, holding
`topic0085`..`topic0188`) reachable from nowhere in the tree, containing
exactly the range of keys `Tree.Lookup` reported missing.

Fix: before performing either the borrow-from-left or merge-into-left
splice, check `left.NextLeaf == leafID`. If it does not hold, release every
latch held and return `(retry=true, nil)` -- the same "benign concurrent
race, retry via a fresh `findParent`" idiom already used elsewhere in this
function (e.g. the `j < 0` case) -- rather than corrupting the chain. By the
time the retry re-reads `parentID`, the pending split's `propagate` call
will typically have completed, and the newly-linked node (not `leftID`)
will correctly be identified as the new left-borrow/merge candidate. The
symmetric borrow-from-right / merge-into-right branches were checked and
found NOT to have this bug: they always adopt `right`'s own freshly-read
`NextLeaf` value directly (`newRight.NextLeaf = right.NextLeaf`), never
assuming a specific target -- and `leafID` (being empty) can never itself
have just been split, so `leafID`'s own adjacency to `rightID` cannot have
been invalidated by an intervening not-yet-linked node.

### Root cause 3: leaf/internal split write order let a lock-free Lookup
    observe a pointer to a not-yet-written new node ("EOF" read errors)

`insertIntoLeafAndPropagate`'s leaf-split path (and `propagate`'s internal-
node-split path) originally wrote the EXISTING node first (`left`, with its
`NextLeaf`/`NextSibling` field already updated to reference the brand-new
right-half node ID) and only then wrote the new node's own content. Under
the pre-2a.4.4 all-latched model this was invisible: no other goroutine
could observe `left`'s updated pointer before both writes (and the
enclosing latch) were released. But `Tree.Lookup` (2a.4.4) is a lock-free
optimistic reader that takes no latch at all -- it can read `left`'s
on-disk bytes the instant they are written and immediately follow the
pointer to the new node, landing on a file offset that has not been written
yet at all (`btree: reading node N at offset ...: EOF`).

Confirmed directly: `TestConcurrentMixedWorkload` under `-race` failed with
exactly this error from a `Lookup` goroutine before this fix.

Fix: reverse the write order in both split paths -- write the brand-new
node (right half) FIRST, then the existing node (left half, whose write is
what actually publishes the new node's reachability) SECOND. This is the
standard "publish last" ordering for a lock-free reader: by the time any
reader can possibly observe a reference to the new node ID, that node's
content is already durably on disk. New-root creation (`propagate`'s
`t.root == childIDBeingReplaced` branch) was checked and already followed
this correct order (writes `newRootID` fully before assigning
`t.root = newRootID`), so no change was needed there.

## Fourth item: a pre-existing over-strict test invariant, not a production bug

Once root causes 1-3 were fixed, one further `TestConcurrentMixedWorkload`
failure remained, from the SHARED `assertStructuralInvariants` test helper
(`insert_test.go`, also used by many `delete_test.go`/`lookup_test.go`
tests): `internal node %d: LowKey = %q, want %q (its own subtree's minimum
key)`, asserting exact equality between an internal node's `LowKey` and its
subtree's current actual minimum key.

This is inconsistent with `InternalNode.LowKey`'s own documented design
(`node.go`): LowKey is "fixed forever once the node is created by a split
(promoted keys are never revised)" -- it is a one-time, immutable-since-
creation lower bound, not a live tracker of subtree content. `Delete`
legitimately shrinks a subtree's true minimum upward over time (by deleting
its own leftmost keys) without ever needing to revise any ancestor's
LowKey, and this is safe: every LowKey-based "move right" peek
(`crabInsertOnce`/`crabDeleteOnce`/`Tree.Lookup`) only ever needs LowKey to
be a valid, never-exceeded lower bound, not an exact one -- a LowKey that is
smaller than the true current minimum just makes "move right" acceptance
slightly more permissive, which is always safe (any key in that gap simply
becomes new, legitimately-placed subtree content if ever inserted). Only a
LowKey that is GREATER than the true minimum would indicate genuine
misrouted content and remains a hard failure.

This was NOT a change to `TestConcurrentMixedWorkload` or
`TestConcurrentMixedWorkloadForcedLookupDuringDelete` themselves (their own
test logic in `btree_test.go` is unmodified, as expected) -- it was a
correction to the shared, over-strict low-level assertion helper they (and
many other pre-existing tests) call, bringing it in line with LowKey's own
documented contract. `assertStructuralInvariants` was changed from
`LowKey != want` (hard fail) to `LowKey > actualMin` (hard fail only when
LowKey is genuinely wrong-direction/misrouted-indicating).
