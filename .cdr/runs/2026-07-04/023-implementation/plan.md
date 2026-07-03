# Plan — task-1.2.3

## 1. Shared descent helper (lookup.go)
Extract `descendToLeaf(store *NodeStore, rootNodeID uint64, path string) (chain []uint64, leaf LeafNode, err error)`:
returns the ordered list of node IDs visited from root to leaf (inclusive),
plus the decoded leaf itself (avoids re-reading the leaf a second time).
Rewrite `Lookup` to call this helper; behavior/signature unchanged.

## 2. NodeAllocator (insert.go)
Mirror `engine/catalog/idalloc.go`'s pattern exactly: a durable monotonic
counter in a sidecar file `<index-file-path>.nodealloc`, single
little-endian uint64 high-water-mark, `WriteAt`+`Sync` before advancing the
in-memory counter, mutex-guarded. IDs start at 1 (0 = `reservedNodeID`,
reused from lookup.go). `NewNodeAllocator(store *NodeStore)` derives the
sidecar path from `store.f.Name()` (same package, unexported field
accessible). Documented as a known gap: no link yet to persisting "current
root ID" -- left to 1.2.5/1.2.6.

## 3. Insert API
`Insert(store *NodeStore, alloc *NodeAllocator, rootNodeID uint64, path string, fileID uint64) (newRootNodeID uint64, err error)`
(one parameter added vs. the issue's suggested signature: an explicit
allocator, since NodeStore intentionally has none per 1.2.2's docs).

Empty-tree bootstrap: if `rootNodeID == reservedNodeID`, allocate a new leaf
node ID, write a single-key leaf, return its ID as `newRootNodeID`. This is
the documented convention extending 1.2.2 (Lookup/NodeStore did not
previously define "no root exists yet" -- this subtask defines it for
Insert; Lookup itself is not required to handle it since a Lookup against a
never-inserted-into tree is out of scope here).

Non-empty tree:
1. `descendToLeaf` to get `chain` + decoded `leaf`.
2. Upsert semantics: if `path` already present in leaf, update `FileIDs[i]`
   in place, re-encode, `WriteNode`, return `rootNodeID` unchanged (no
   structural change possible from an update).
3. Otherwise insert new key in sorted position (`sort.Search`-based
   insertion into a copy of Keys/FileIDs).
4. If `leafEncodedSize(newLeaf) <= NodeSize`: write in place at the leaf's
   existing node ID, return `rootNodeID` unchanged.
5. Else split: choose a split index via `chooseLeafSplit` (half-by-count,
   defensively shifted using `leafEncodedSize` until both halves fit).
   Left half stays at the original leaf's node ID; right half gets a new ID
   from `alloc.Next()`. Link `left.NextLeaf = rightID`,
   `right.NextLeaf = <original leaf's old NextLeaf>`. Promote
   `right.Keys[0]` (duplicated, per standard B+Tree leaf-split semantics)
   as the new separator with `rightID` as the new child pointer.
6. Propagate up the `chain` (from the leaf's parent upward): at each level,
   locate the existing child slot that pointed at the node ID that was just
   split/rewritten (`childIDBeingReplaced`, which never changes since the
   "left" half always keeps its original node ID), insert the pending
   separator key immediately after it and the pending new child ID
   immediately after that (preserving the `Children[i]` covers
   `[Keys[i-1],Keys[i])` invariant). If the resulting internal node fits
   within `NodeSize`, write it in place and stop (root ID unchanged). If it
   overflows, split it via `chooseInternalSplit`: the median key is
   promoted alone (NOT duplicated, per standard internal-split semantics,
   distinct from the leaf case) to the next level up; left half keeps its
   original node ID, right half gets a new allocated ID; continue
   propagating with the promoted key/new right ID as the new pending
   separator/child.
7. If propagation reaches past the top of the chain (the old root itself
   was split, or the tree was a single leaf that split with no parent at
   all), allocate a brand-new root internal node with
   `Keys=[pendingSeparator]`, `Children=[childIDBeingReplaced, pendingChildID]`
   and return its ID as `newRootNodeID`.

## 4. Tests (insert_test.go)
- `TestInsertEmptyTree`: single insert into a brand-new tree (rootNodeID=0),
  verify via `Lookup`.
- `TestInsertLeafSplit`: sequential inserts with an artificially tiny
  effective capacity is not needed -- real `NodeSize` (4096) easily forces a
  leaf split with a modest number of realistic topic-path keys (documented
  choice: use real `NodeSize`, not a shrunk test-only constant, to keep the
  test exercising the exact same code path as production). Verify: leaf
  split occurred (root becomes internal, or a second-level split appears),
  and every inserted key remains lookup-able via `Lookup`, never-inserted
  keys are not found.
- `TestInsertInternalSplit`: enough inserts (more paths, still realistic
  short topic-path strings) to force at least one internal-node split too,
  producing an internal node with >= 2 separator keys (closes 1.2.2's
  flagged gap). Verify full-tree `Lookup` correctness for all inserted keys
  plus a handful of never-inserted keys, using the real `Insert` path only
  (no `buildTestTree` reuse).
- Structural invariant checks: walk the resulting tree (via `ReadNode`) and
  assert internal node keys are sorted ascending and leaf `NextLeaf` chain
  reaches every leaf left-to-right with keys in global sorted order
  (closes "tree remains balanced" / "correct fanout" acceptance criterion).

## 5. Self-consistency
`go build ./engine/...`, `go vet ./engine/...`,
`go test ./engine/btree/... -race -v` (full package: 1.2.1's
`TestNodeSerialization*`, 1.2.2's `TestLookup`, and this subtask's new
tests must all pass with no regression).
