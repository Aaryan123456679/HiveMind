# Architecture Discovery — Subtask 1.2.4

## Read order followed

1. `.cdr/memory/*` (state.md, decisions.md, pending.md, impact-map.md, timeline.md,
   regression-routes.md) — all empty scaffolds, no prior carry-forward notes blocking this run.
2. `docs/HLD.md` (system context) and `docs/LLD/btree.md` (module LLD) — see requirement.md for
   what LLD says/doesn't say about delete/rebalancing.
3. `.cdr/index/file.jsonl` — confirms `engine/btree/node.go`, `lookup.go`, `insert.go` and their
   tests are the only existing btree files, all tagged from verified runs.
4. `.cdr/index/task.jsonl` — confirms task-1.2.1, task-1.2.2, task-1.2.3 all `state: verified`.
5. `engine/btree/node.go` (full) — node layout, `NodeSize=4096`, `LeafNode`/`InternalNode`
   structs, `Encode`/`Decode*`, `leafEncodedSize`/`internalEncodedSize`, `noSibling=0` sentinel.
6. `engine/btree/lookup.go` (full) — `NodeStore` (`ReadNode`/`WriteNode`), `reservedNodeID=0`,
   `descendToLeaf` (returns full descent chain + decoded leaf), `Lookup`.
7. `engine/btree/insert.go` (full) — `NodeAllocator` (monotonic `Next()`, sidecar
   `.nodealloc` file, no free-list — documented known gap), `Insert` (empty-tree bootstrap,
   upsert-in-place, leaf split via `splitLeaf`/`chooseLeafSplit`, propagation via
   `propagateSplit`/`insertIntoInternal`/`splitInternal`/`chooseInternalSplit`, new-root
   allocation on top-level split).
8. `engine/btree/insert_test.go` (full) — test scaffolding conventions to mirror:
   `newTestStoreAndAllocator`, `genKey`/`insertN` (build trees via the real `Insert` path, not
   synthetic node construction), `assertAllLookupable`, `assertStructuralInvariants` (sorted
   keys, correct fanout, leaf-chain `NextLeaf` traversal visits every key once in sorted order),
   and the `TestXxxSplit` -> `t.Run(...)` dispatch pattern for the literal `-run TestInsertSplit`
   acceptance name.

## Key design constraints discovered

- `descendToLeaf` returns the full root-to-leaf `chain []uint64`; `Delete` must reuse this
  (per task instructions) instead of re-implementing descent. However, `descendToLeaf` does not
  currently give access to each ancestor's *decoded* `InternalNode` along the way — only node
  IDs. `Delete`'s underflow-repair logic needs each ancestor's key/child index to find left/right
  siblings, so it will re-`ReadNode` each ancestor already present in `chain` (cheap: single
  `ReadAt` per level, same as `propagateSplit` already does for split-propagation instead of
  caching decoded nodes from descent).
- No existing "underflow threshold" constant. Chosen convention (documented in delete.go):
  "less than half of the split threshold's natural fill" is not directly expressible for
  variable-length keys (unlike a slot-count B-tree), so instead of a fixed fraction of `NodeSize`,
  this implementation uses a simple, defensible policy: a leaf/internal node is "underflowing"
  when it has fewer than `minLeafKeys` / `minInternalKeys` = 1 key (i.e. as soon as a leaf/
  internal node would become completely empty of keys, OR — for leaves specifically — whenever a
  sibling has strictly more keys than it and the sibling can spare one without itself going
  below that same floor). This mirrors "less than half full" in spirit for a small, deterministic,
  easy-to-test threshold (real halves-of-4096-bytes computation exists for split; using an
  analogous byte-size threshold for underflow-detection is used at leaf level: `leafEncodedSize`
  less than half NodeSize triggers borrow/merge consideration). Documented explicitly in
  delete.go's doc comments.
- `NodeAllocator` has no free-list (1.2.3's documented known gap). Per task guidance, this
  subtask documents merged-away node IDs as simply abandoned/orphaned in the allocator's ID
  space (never reused, never explicitly freed) — consistent with "likely to be revisited when
  persist/reload (1.2.5/1.2.6) is built." No new reuse mechanism added.
- Root-collapse: if, after a merge, the internal root node ends up with exactly 1 child (0 keys),
  that child becomes the new root (returned as `newRootNodeID`), mirroring `Insert`'s existing
  "callers must use the returned newRootNodeID" contract.
- Empty-tree convention: if deleting the last key anywhere empties the (single) root leaf, the
  tree's root node is left as that now-empty leaf (rootNodeID unchanged, still a valid — if
  key-less — leaf node), NOT reset to `reservedNodeID`. Rationale: `reservedNodeID` (0) is
  `Insert`'s sentinel for "bootstrap a brand new tree, allocate a new leaf"; silently reusing it
  here would force `Delete`'s caller to distinguish "never had a tree" from "tree now empty" via
  some other signal, which the issue's acceptance criteria don't require. Documented explicitly
  in `Delete`'s doc comment as the chosen empty-tree-after-delete convention (distinct from
  `Insert`'s empty-tree-before-any-insert convention).
