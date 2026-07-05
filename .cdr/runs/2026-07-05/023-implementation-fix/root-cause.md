# Root Cause

## The bug

`Tree.propagate` inserts a promoted separator key (from a leaf or internal
node split) into the parent internal node using `insertIntoInternal(parent,
j, promotedKey, newChildID)`, where `j = indexOfChild(parent.Children,
childIDBeingReplaced)` -- a purely *positional* index (the current index of
the node that just split, within its parent's `Children` slice), not a
position derived from `promotedKey`'s own sorted value.

This is exploitable when a single parent `P` has two of its children split
and promote concurrently before either promotion is applied to `P`. Because
`findParent`'s leaf-chain-walk (from the round-1 fix) and its per-parent
`Lock`/`ReadNode`/retry-on-`j<0` loop only guarantee that, at the moment a
propagation is *applied*, `childIDBeingReplaced` is directly present in the
freshly-read `parent.Children` at some index `j` -- they do **not** guarantee
that `j` is `promotedKey`'s correct sorted position once other, independent
promotions into the same parent have already landed at nearby positions in
a different order than the keys' true sort order would require. Two
separately-split children of `P`, both inserting positionally relative to
their own prior position, can end up placing their promoted keys into `P`
out of key order -- corrupting `P.Keys`' sortedness. Once `P.Keys` is
unsorted, `sort.Search`/`sort.SearchStrings`-based routing used by `Lookup`
and further descents silently routes some already-inserted keys to the
wrong child, so they become permanently unreachable (never surfaced as an
error -- `Lookup` just reports "not found").

## Confirming the fix is correct (not just plausible)

1. Read `Tree.propagate` in full (not just the diff hunk): the surrounding
   per-parent `Lock`+`ReadNode`+retry-on-`j<0` loop and `findParent`'s
   leaf-chain-walk from the round-1 fix were re-read to confirm the failure
   mode is genuinely reachable and not already precluded by existing
   locking. It is: `j` is freshly computed under the parent's lock, but
   freshness of `j` only guarantees `childIDBeingReplaced`'s *current*
   position, not that inserting immediately after it is *key-order-correct*
   relative to other already-applied sibling promotions.

2. Counterfactual empirical test: reverted `engine/btree/insert.go` to the
   original (round-1-only) code via `git stash` and ran the 160g/80k repro
   harness (`TestZZReproSilentDataLoss`) 15 times. Result: 3/15 (20%)
   failures, all with the identical signature -- an internal node with
   `Keys` demonstrably out of sorted order (confirmed via the harness's
   diagnostic walk) and a nonzero count of missing keys via `Lookup`. This
   matches the 022-verification finding (~8.6%, 3/35) within expected
   sampling variance and confirms the failure mode is real and matches the
   stated root cause (not a coincidental separate bug).

3. With the fix restored and applied, the same harness was run 30+30+... =
   79 total clean runs (across several batches, described in
   self-consistency.json) with zero failures, zero unsorted-Keys
   detections, and zero missing keys.

## The "`pos < j` should be unreachable" defensive fallback

The in-progress fix included a defensive guard: if `sort.Search`'s computed
position `pos` is ever less than the old positional index `j`, fall back to
`pos = j` (the old, buggy behavior), reasoning that this should be
mathematically unreachable given `promotedKey` is always greater than
`parent.Keys[j-1]` (if any).

This reasoning is correct **given `parent.Keys` is actually sorted** at the
time `sort.Search` runs (that is `sort.Search`'s own precondition). But if
`parent.Keys` were ever to become unsorted for any reason (e.g. this exact
class of bug manifesting at a different node, or some other not-yet-found
interaction), `sort.Search`'s binary search over unsorted data is undefined
and `pos` could come out arbitrary, including `< j`. In that scenario, the
original fallback would have **silently reintroduced the exact positional
bug this fix is meant to close**, defeating the point of a "defensive"
guard. This was corrected during this run: the fallback now returns a
distinct `btree: internal invariant violated` error (unlocking the parent
first) instead of silently falling back, so any future violation of this
invariant fails loudly and traceably rather than silently regressing. This
polish did not change behavior on any of the 79+ clean validation runs.

## Conclusion

The round-2 fix's stated root cause is confirmed correct and the fix is
confirmed sufficient: sorting the promoted key's insertion position via
`sort.Search` over `parent.Keys` (rather than a stale positional index) makes
the outcome depend on key order rather than on the scheduling order of
concurrent same-parent promotions, which is exactly the property needed to
keep `parent.Keys` sorted regardless of which of several concurrent
same-parent splits is applied first.
