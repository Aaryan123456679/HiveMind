# Plan — task-2a.4.2

## Crabbing discipline chosen: window-of-2 (literal), not full optimistic descent

Per the acceptance criterion's literal phrasing ("no writer ever holds more
than a parent+child latch pair at once"): at every instant, hold at most 2
node latches. Concretely:

- **Descent** (root -> leaf): lock `root`. Loop: read node; if internal,
  compute child index by path comparison, `Lock(child)` (now holding 2:
  parent+child), then immediately `Unlock(parent)` (back down to 1), before
  looping to read `child`. Reach the leaf still holding exactly its own
  latch (1).
- **Leaf mutation**: still holding the leaf's latch, insert (or upsert) in
  place. If it fits, write + unlock, done. If it overflows: split, write
  left half back to the same ID (still holding its latch), `Lock` the
  brand-new right-sibling ID only for its own write (2 held: leaf+sibling,
  both freshly allocated/uncontended), unlock sibling, unlock leaf.
- **Split propagation**: never trust a precomputed ancestor-ID chain across
  the propagation walk (see "root-split & shared-parent races" below) —
  instead, for each level, freshly locate the *current* direct parent of the
  node that just split (`findParent`, an independent root-to-`childID`
  crabbing walk, itself window-of-2) via the same key-routing used for
  descent, lock it, mutate, write, unlock, before moving to the next level
  up. At most 1 ancestor latch is held at a time during propagation (plus a
  transient 2nd for a freshly split-off sibling), comfortably inside budget.
- Lock ordering is always root-to-leaf / outer-to-inner, both in descent and
  in `findParent`'s re-derivation — never leaf-to-root — so no cycle can
  form against a concurrent op walking the same direction. Every `Lock` has
  a matching `Unlock` on every return path (including error paths); no
  `defer`-stack is used because the held set is a sliding window rather than
  a strict stack, so each function unlocks explicitly before every return.

## Root-split & shared-parent races

- **Root pointer protection**: new `Tree.rootMu sync.Mutex` + `Tree.root
  uint64`, deliberately NOT the same mechanism as any per-node `nodeLatch`
  (root-ness is tree-level state with no on-disk identity of its own).
  `rootMu` is held only for the brief "is `childID` still the current root?
  if so, allocate+install a new root" check-and-commit, not for the whole
  insert or the whole propagation walk (so disjoint-subtree inserts never
  contend on it except during the rare, real root-split instant).
- **Concurrent root-split race** (two inserts both propagate all the way to
  what they each believe is "the root" at nearly the same time): whichever
  one takes `rootMu` first and finds `Tree.root == childID` wins and installs
  the new root. The other, finding `Tree.root != childID` under `rootMu`,
  falls back to `findParent(currentRoot, path, childID)` — since node IDs
  are *never* reparented in this package (a split only ever creates a new
  *sibling* ID, the original ID's parent lineage is otherwise unchanged),
  `childID` is always still reachable by descending via `path`'s normal
  key-routing from whatever the *current* root now is, however many
  promotions have happened. This uniformly handles both "ordinary ancestor
  still exists, just go find it" and "root got replaced" without needing
  two separate code paths.
- **Concurrent shared-parent-split race** (two inserts both need to update
  the *same* ancestor, and one has already split it out from under the
  other by the time the second acquires its latch): detected via
  `indexOfChild` returning -1 after locking the parent found by
  `findParent`; handled by retrying (loop back to a fresh `findParent` call)
  rather than erroring, since `findParent` always finds the *current*
  correct parent regardless of how many times it has been split/replaced.

## Empty-tree bootstrap
`Tree.Insert` handles `rootNodeID == reservedNodeID` itself, under `rootMu`
held for the whole (rare, one-shot) bootstrap: allocate the first leaf,
write it (latched), and install it as the root. A second concurrent
bootstrapper blocks on `rootMu` and, once unblocked, observes the
now-installed root and proceeds through the normal path instead.

## Tests (`TestCrabbingInsert` in insert_test.go)
1. **Disjoint subtrees**: pre-build a moderately sized tree (several
   hundred keys, multiple leaves) single-threaded via the existing free
   `Insert`, wrap it in a `Tree`, then run N goroutines each inserting into
   its own far-apart, non-overlapping key range concurrently. Assert (under
   `-race`) no failures and, after `Wait()`, every inserted key is
   look-up-able via `Lookup(tree.Store, tree.Root(), key)` with the correct
   fileID, plus `assertStructuralInvariants`.
2. **Overlapping/shared subtree**: start from a *small* or empty tree so
   many goroutines' keys interleave and route through the same internal
   nodes/leaves, forcing real contention and (with enough keys) multiple
   concurrent leaf and/or internal splits, very plausibly including
   concurrent root splits. Assert the same postconditions.
3. Both subtests run under `t.Run` inside `TestCrabbingInsert` so `-run
   TestCrabbingInsert` (the literal test-spec command) exercises both.

## Amendment: Blink-tree move-right recovery (discovered during implementation)

The plan above, taken literally, has a correctness gap the acceptance
criteria's own phrasing anticipates but does not fully resolve: releasing
an ancestor's latch *before* a split it is about to require has been
propagated back up to it means a concurrent writer descending via that
momentarily-stale ancestor can be routed to a node whose upper key range
has already been split off into a new right sibling. Left unhandled, this
silently strands inserted keys in the wrong (now-disconnected-from-that-
range) node -- confirmed via a targeted, deliberately small/deterministic
repro before this fix (2 goroutines, several hundred keys each, looped
until failure).

Fix (Lehman & Yao Blink-tree technique), additive to the plan above, no
change to the 2-latch budget:
- `InternalNode` gains `NextSibling uint64` (mirrors `LeafNode.NextLeaf`)
  and `LowKey string` (the fixed-forever, smallest key reachable in the
  node's subtree; NOT the same as `Keys[0]`, which is a separator one
  level further down -- see node.go's doc comment on `LowKey` for why using
  `Keys[0]` or the node's own currently-populated max key both under/over-
  correct whenever a node's occupied range has gaps, e.g. under concurrent
  out-of-order inserts).
- Both `crabInsert` and `findParent`'s descent loops, at every node visited
  (leaf or internal), peek the right sibling (leaf: its first key; internal:
  its `LowKey`) *before* making any routing decision; if the target key is
  `>=` that peeked lower bound, move right (unlock current, lock sibling,
  repeat) before proceeding. This is a bounded number of *additional*
  lock/unlock pairs layered on top of the same window-of-2 discipline (at
  most current+candidate-sibling held simultaneously, same as descent's
  parent+child), not a relaxation of the 2-latch budget.
- `splitInternal`/`insertIntoInternal`/the single-threaded `propagateSplit`/
  `Tree.propagate` all correctly wire `NextSibling`/`LowKey` when creating or
  copying internal nodes (left keeps its own `LowKey`; right's `LowKey`
  becomes the promoted key).

## Self-consistency (not verification)
- `go build ./...`, `go vet ./...`, `gofmt -l` clean.
- `go test ./engine/btree/... -race -v -count=1` green, all pre-existing
  tests included (no regressions).
- `go test ./engine/btree/... -race -run TestCrabbingInsert -count=5` for
  flakiness.
