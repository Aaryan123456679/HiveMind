# Architecture Discovery — task-1.2.3

## Read order followed
1. `.cdr/memory/*.md` — all empty (no prior decisions/state relevant yet).
2. `docs/HLD.md` — `engine/btree/` = on-disk B+Tree, latch-crabbing writes /
   optimistic reads (concurrency deferred per LLD). No insert-specific detail
   beyond module table.
3. `docs/LLD/btree.md` — status "scaffold only"; lists Insert as an operation
   but gives no split-point/promotion convention beyond "standard B-Tree
   crabbing" for concurrency (explicitly deferred). No LLD guidance beyond
   HLD/issue text, so this run follows standard B+Tree insert-with-split
   semantics per the issue's design guidance.
4. `.cdr/index/file.jsonl` / `.cdr/index/task.jsonl` — confirmed `task-1.2.1`
   and `task-1.2.2` are `verified` (PASS / PASS_WITH_COMMENTS respectively).
   1.2.2's verification comment: internal nodes with >=2 keys untested.
5. `engine/btree/node.go` (full) — `LeafNode{Keys, FileIDs, NextLeaf, Version}`,
   `InternalNode{Keys, Children, Version}`, `NodeSize = 4096`,
   `leafEncodedSize`/`internalEncodedSize` helpers, `Encode`/`Decode` pairs,
   `noSibling = 0` sentinel for rightmost leaf.
6. `engine/btree/lookup.go` (full) — `NodeStore{f *os.File}` wraps
   `OpenIndexFile`'s handle; `reservedNodeID = 0` (never valid, addressing
   starts at node ID 1, `offset = nodeID * NodeSize`); `ReadNode`/`WriteNode`;
   `Lookup(store, rootNodeID, path)` descends via "first key strictly greater
   than path -> child before it" covering convention. Explicit doc comment:
   NodeStore "deliberately does NOT implement an allocator or free-list" —
   this subtask must add one.
7. `engine/btree/lookup_test.go` (full) — `buildTestTree` is test-only
   scaffolding building a fixed 3-level tree with hand-assigned node IDs;
   confirmed NOT to reuse for the real insert path (per its own doc comment
   and per task instructions).
8. `engine/catalog/idalloc.go` (full) — reference pattern for a durable
   monotonic ID allocator: sidecar file next to the main data file
   (`<path>.idalloc`), single little-endian uint64 high-water-mark,
   `WriteAt` + `Sync` before advancing in-memory counter, mutex-guarded,
   IDs start at 1 (0 reserved). Reused this idiom for node-ID allocation.

## Key existing conventions to preserve
- Node ID 0 (`reservedNodeID`) is reserved/never valid; real IDs start at 1.
  A brand-new/empty tree therefore has no existing root node, and the
  existing `Lookup`/`NodeStore` API from 1.2.2 does **not** define a
  convention for "empty tree" (it always assumes `rootNodeID` addresses a
  real, already-written node). This subtask must define and document that
  convention for `Insert`'s empty-tree bootstrap case (see plan.md).
- `Lookup`'s descent loop (root -> ... -> leaf, "first key > path -> child
  before it") must not be duplicated; factored into a shared
  `descendToLeaf` helper in `lookup.go` used by both `Lookup` and `Insert`.
- Internal-node covering convention: `Children[i]` covers `[Keys[i-1],
  Keys[i])`; splitting/insertion logic must preserve this invariant exactly
  (no off-by-one on which side of a new separator a child lands).
- Leaf split: separator key IS duplicated (remains in the leaf, promoted
  copy goes to parent). Internal split: separator key is NOT duplicated
  (removed from both children, promoted alone to grandparent) — per the
  task's explicit design guidance, this is the standard distinction that
  must be implemented correctly.
