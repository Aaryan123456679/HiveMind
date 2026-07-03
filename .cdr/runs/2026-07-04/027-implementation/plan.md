# Plan — Subtask 1.2.4

## API

```go
func Delete(store *NodeStore, alloc *NodeAllocator, rootNodeID uint64, path string) (newRootNodeID uint64, found bool, err error)
```

`alloc` is accepted for API symmetry with `Insert` (and in case a future revision adds free-list
reuse of abandoned node IDs) but is currently unused: `Delete` never allocates a new node, it only
rewrites/repurposes existing ones.

## Chosen "simplified rebalancing" strategy (documented per issue's own wording)

The issue's checklist title literally suggests a "merge-or-tombstone strategy, documented choice".
This run adopts:

- **Tombstone policy**: a leaf (or internal node) that still holds >= 1 key after a deletion is
  left alone, even if under-capacity ("half full" or less). No eager rebalancing on partial
  underflow.
- **Repair trigger**: rebalancing (borrow-or-merge) is triggered only when a node becomes
  *completely empty of keys* (0 keys) as a direct result of a deletion.
- **Leaf repair**: try to borrow one key from the left sibling (if it has > 1 key), else the right
  sibling (if it has > 1 key), else merge with a sibling (left preferred), fixing up `NextLeaf`
  links and the parent's separator key / child pointer.
- **Internal repair**: only reachable when a leaf-merge causes the leaf's parent to drop from 1
  key (2 children) to 0 keys (1 child) -- i.e. genuinely degenerate. That parent is spliced out of
  *its* parent (grandparent) by redirecting the grandparent's child pointer straight to the
  degenerate parent's single surviving child. This never changes the grandparent's own key/child
  count, so no further propagation above that point is ever required -- bounded to at most 2 hops
  above the leaf (parent shrink, then optionally one grandparent splice or root collapse).
- **Root collapse**: if the *root* itself is the node that shrinks to 0 keys (1 child), that child
  becomes the new root, returned as `newRootNodeID`.
- **Empty-tree-after-delete convention**: if the leaf being deleted from IS the root (single-node
  tree) and it becomes empty, `rootNodeID` is returned unchanged (still points at a valid,
  zero-key leaf) -- NOT reset to `reservedNodeID`. This is a deliberate, documented divergence
  from `Insert`'s "reservedNodeID means bootstrap a new tree" convention, since `Delete`'s caller
  already has a real root node ID and resetting it would require an extra out-of-band signal not
  required by the issue's acceptance criteria.
- **Abandoned node IDs**: nodes eliminated by a merge or splice are never reused or explicitly
  freed (no free-list exists per 1.2.3's documented known gap on `NodeAllocator`). This is
  explicitly accepted as a known gap, expected to be revisited alongside persist/reload
  (1.2.5/1.2.6).

## Files to add

- `engine/btree/delete.go` -- `Delete`, `removeFromLeaf`, `repairEmptyLeaf`,
  `shrinkParentAfterMerge`.
- `engine/btree/delete_test.go` -- `TestDelete` (literal acceptance-test entry point per the
  issue's `-run TestDelete` test spec), dispatching subtests for: empty tree, absent key,
  single-leaf (no rebalancing), leaf merge/redistribute, internal merge/redistribute, and a full
  insert+delete+lookup integration check.

## Reuse of existing verified primitives

- Descent: reuse `descendToLeaf` (lookup.go) verbatim, exactly as `Insert` does -- no duplicated
  traversal logic.
- Node I/O: reuse `NodeStore.ReadNode`/`WriteNode`, `writeLeaf`/`writeInternal` (insert.go).
- Sizing/encoding: no new encoding helpers needed (merges in this design never grow a node beyond
  a sibling's pre-existing size, so `leafEncodedSize`/`internalEncodedSize` overflow checks are not
  required on the merge path; noted as a documented known limitation on the *borrow* path, where a
  replacement separator key of different length than the original could theoretically overflow an
  internal parent -- accepted as out of scope for this subtask's test spec, which uses
  uniform-length keys).

## Test plan (mirrors insert_test.go conventions: real `Insert`-built trees, not synthetic ones)

1. `TestDeleteEmptyTree` -- `Delete` against `reservedNodeID`: `found=false`, no error, no panic.
2. `TestDeleteAbsentKey` -- non-empty tree, delete a key never inserted: `found=false`, tree
   unchanged, structural invariants still hold.
3. `TestDeleteSingleLeaf` -- small tree (single leaf, no split), delete one key: `found=true`,
   deleted key not lookup-able, remaining keys lookup-able, structural invariants hold.
4. `TestDeleteLeafMerge` -- build a real multi-leaf tree via `Insert`, then delete an entire
   contiguous leaf's worth of keys (driving that leaf to exactly 0 keys) to force a real
   merge-or-borrow at the leaf level; assert structural invariants + correct lookups afterward.
5. `TestDeleteInternalMerge` -- build a large real tree (multiple internal levels via `Insert`,
   analogous to `TestInsertInternalSplit`'s `n=2000`), then delete enough keys to drive an
   internal node to degenerate (0 keys) and trigger the grandparent-splice/root-collapse path;
   assert structural invariants + correct lookups afterward.
6. `TestDeleteInsertLookupIntegration` -- mixed insert/delete/lookup sequence: every remaining key
   found via `Lookup` with correct fileID, every deleted key not-found.
7. `TestDelete` -- literal acceptance-test entry point (`-run TestDelete`), `t.Run`-dispatches all
   of the above so the issue's exact test-spec command exercises real coverage.

## Structural-invariant checker extension

Reuse `assertStructuralInvariants` from insert_test.go where possible; add a companion
`assertNoOrphanedPointers` (or extend it) validating: every child pointer reachable from the root
decodes successfully (no dangling reference to an abandoned node ID that got accidentally
re-referenced), and the leaf-chain traversal (`NextLeaf`) still visits every remaining key exactly
once in sorted order after deletions.
