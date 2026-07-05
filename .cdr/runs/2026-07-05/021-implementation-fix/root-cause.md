# Root Cause

## Reproduction (done before any fix, per task step 2)

Built a scratch adversarial test (64 goroutines, ~30,080 keys, interleaved
striped assignment identical in spirit to `testCrabbingInsertOverlappingSubtree`
but at the verifier's scale) and ran it repeatedly against the unmodified
`eff41a0`/`edd81f9` code under `-race`. Reproduced:

```
btree: internal invariant violated: findParent reached leaf 36 while
searching for the current parent of 37 along path "topic3419/page"
```

at attempt 3 and again at attempt 12 across two separate runs, confirming
the verifier's finding (~1-in-20 to 1-in-25 rate) is real, not a test
artifact.

Diagnostic instrumentation on that reproduction showed, at the moment of
failure:
- root (node 3) is a single internal node whose `Children` ends at node 36.
- node 36 decodes as a **leaf** (not internal) with 0 keys shown in the
  generic trace (a display quirk of the diagnostic dump, not a real bug --
  the actual leaf content lives in the separate `leaf` return value).
- `childID` (37) is a **leaf** with 92 keys and `NextLeaf = 38`.

## Actual root cause (neither of the verifier's two stated hypotheses)

The verifier's `confirmed_bugs` entry offered two candidate hypotheses:
(a) a race between an internal node's own concurrent split and a
grandparent's concurrent promotion invalidating findParent's routing
invariant, or (b) the internal-level move-right `LowKey` peek misrouting
around a legitimately-empty `LowKey`.

Neither is what actually happens. The real defect: **`findParent` has an
internal-level move-right/recovery mechanism (via `InternalNode.NextSibling`
and `LowKey`) but has *no* equivalent recovery mechanism at the leaf level**,
even though `crabInsert` itself has always needed (and has) a leaf-level
move-right peek via `LeafNode.NextLeaf`.

Under real concurrency, many goroutines can rapidly, repeatedly split the
*same* leaf (and its immediate right-hand successors) faster than each
split's own `propagate` call can link the new sibling into their shared
ancestor. This produces a chain of not-yet-linked leaves
(`36 -> 37 -> 38`, connected via `NextLeaf`) hanging off a single ancestor
`Children` entry that still only points at the *oldest* leaf in that chain
(36). When the `propagate` call responsible for linking leaf 37 into the
ancestor invokes `findParent(root, path, childID=37)`, the walk correctly
routes down to node 36 (the ancestor's current, valid registration) --
finds it is a leaf, is not equal to `childID` (37), and, in the original
code, **immediately declares a hard invariant violation** with zero
recovery, even though `childID` (37) is perfectly reachable two `NextLeaf`
hops further along from 36.

This is a distinct, more fundamental gap than either verifier hypothesis:
`findParent`'s own doc comment assumed `childID` would always be reachable
via ordinary top-down routing, but never accounted for the possibility that
`childID` itself is a leaf several splits ahead of the leaf its shared
ancestor currently has registered.

## Fix

`findParent`'s `isLeaf` branch now walks the `NextLeaf` chain from the
landed-on leaf, looking for `childID`, mirroring `crabInsert`'s own
leaf-level move-right peek and the existing internal-level `NextSibling`
recovery loop already present in `findParent`. If `childID` is found along
that chain, the original ancestor (tracked via a new `ancestorID` variable,
updated at the point of each top-down descend step) is returned as the
correct (and only) parent -- since only the ancestor's `Children` slice
needs a new entry, regardless of how many not-yet-linked leaf splits sit
between its registered child and `childID`. If the chain is exhausted
(`NextLeaf == noSibling`) without finding `childID`, or the chain leads to a
non-leaf node, a clear, distinct invariant-violation error is still
returned (bounded by the finite leaf chain -- no unbounded retry needed,
since this is a pure local recovery analogous to the sibling case).

A defensive guard was also added: if `rootID` itself decodes as a leaf on
the very first iteration (i.e. `ancestorID` was never set because we never
descended out of any internal ancestor), that is treated as a genuine,
distinct invariant violation rather than silently returning `ancestorID`'s
zero value, since a bare-leaf root reaching `findParent` at all (rather than
being handled by `propagate`'s own root-promotion branch) would indicate an
unrelated caller bug.

See `engine/btree/insert.go`, function `Tree.findParent`.
