# Architecture Discovery — Subtask 1.2.2

## Indexes / memory read (in order)

- `.cdr/memory/decisions.md`, `pending.md`, `state.md`, `impact-map.md` — all empty
  scaffolds at time of this run; nothing pre-recorded for btree.
- `docs/HLD.md` — system context only, no btree-specific addressing detail.
- `docs/LLD/btree.md` — confirms: on-disk B+Tree at `index/name.idx`, operations
  (lookup/scan/insert/delete), concurrency model (latch-crabbing writes, optimistic
  lock-free reads via version counter — NOT implemented yet, reserved by 1.2.1).
  Does NOT specify a node-ID-to-offset addressing scheme explicitly; leaves it to
  implementation, consistent with catalog's `pageID * PageSize` precedent.
- `.cdr/index/file.jsonl` — confirms `engine/btree/node.go` (1.2.1, verified) and
  `engine/btree/node_test.go` are the only btree source files so far.
- `.cdr/index/task.jsonl` — `task-1.1.1`..`task-1.1.5` (catalog) verified;
  `task-1.2.1` verified (commit a9815aa / node layout+serialization, +1 follow-up
  commit 3364ada for the Version field). `task-1.2.2` not yet present as an entry
  (will be added by this run).

## Source read: `engine/btree/node.go` (full)

- `NodeSize = 4096`. Comment at top of file already states the intended convention
  explicitly: "a node index maps directly to a byte offset (index * NodeSize) within
  the index file" — this settles the node-ID-to-offset question: **nodeID * NodeSize**,
  directly analogous to `engine/catalog/file.go`'s `pageID * PageSize`.
- `noSibling uint64 = 0` sentinel, with comment: "Real node/page IDs are allocated
  starting at 1 by later subtasks, mirroring engine/catalog's convention that ID/page
  0 is reserved rather than a valid data unit." So node ID 0 is reserved (analogous to
  catalog's free-list page 0); real nodes start at ID 1.
- `LeafNode{Keys []string, FileIDs []uint64, NextLeaf uint64, Version uint64}`,
  `InternalNode{Keys []string, Children []uint64, Version uint64}`.
  `InternalNode` doc comment states the exact covering convention: "Children[i] holds
  all keys < Keys[i] for i==0, keys in [Keys[i-1], Keys[i]) for interior children, and
  keys >= Keys[n-1] for the last child" — this is the descent rule Lookup must follow.
- `Encode()` / `DecodeLeafNode()` / `DecodeInternalNode()` round-trip a node to/from
  exactly `NodeSize` bytes (zero-padded). `DecodeLeafNode` on internal-typed bytes (and
  vice versa) returns an error (type discriminator check) — this is how a `NodeStore`
  reader can dispatch on node type without a separate on-disk tag file.
- `OpenIndexFile(path) (*os.File, error)` creates-or-opens the backing file; does NOT
  wrap it in any page/node-store abstraction yet (1.2.1 was pure in-memory
  encode/decode + file-creation-on-first-use only). This subtask must add the missing
  file-I/O layer (read/write a node by ID) — this is the "NodeStore" gap referenced in
  the task brief.

## Source read: `engine/catalog/file.go` (relevant portion)

- Confirms the precedent this subtask should mirror in *spirit* but narrower in
  *scope*: `FileManager` wraps `*os.File`, addresses pages by `pageID * PageSize`
  byte offset, page 0 reserved. However `FileManager` also implements a free-list
  bitmap allocator — that is explicitly NOT needed yet for btree, since 1.2.2 only
  needs to *read* nodes that a test helper wrote by simple sequential appending
  (`nodeID` 1, 2, 3, ... in file order). A full allocator is deferred to whichever
  later subtask's LLD calls for it (likely bundled into 1.2.3's real insert, if
  needed at all — nodes may simply grow the file monotonically since there is no
  delete-driven reclamation requirement yet).

## Decision: NodeStore scope

- `NodeStore` = thin wrapper: `struct { f *os.File }`, with:
  - `ReadNode(nodeID uint64) (isLeaf bool, leaf LeafNode, internal InternalNode, err error)`
    — seeks to `nodeID * NodeSize`, reads exactly `NodeSize` bytes, peeks the type
    discriminator byte (first byte) to decide whether to call `DecodeLeafNode` or
    `DecodeInternalNode`.
  - `WriteNode(nodeID uint64, encoded []byte) error` — seeks to `nodeID * NodeSize`,
    writes exactly `len(encoded)` bytes (callers pass the output of `.Encode()`,
    which is already exactly `NodeSize` bytes).
  - No allocator, no free-list, no locking (concurrency is a later subtask per LLD).
  - Built on top of the existing `OpenIndexFile` (no change to node.go's public API
    needed; NodeStore lives in the new `lookup.go`, or could be node.go — chosen to
    live in `lookup.go` since it exists purely to serve Lookup's traversal needs and
    the test-scaffolding writer; this keeps 1.2.1's file (node.go) unchanged/frozen).

## Decision: Lookup traversal

- `Lookup(store *NodeStore, rootNodeID uint64, path string) (fileID uint64, found bool, err error)`.
- At an internal node: find the first key index `i` such that `path < Keys[i]`
  (i.e. `sort.Search`); descend into `Children[i]` (if no such `i`, i.e. path >= all
  keys, descend into the last child `Children[len(Keys)]`). This matches the exact
  covering convention documented on `InternalNode` above.
- At a leaf: scan/binary-search `Keys` for an exact match; if found return
  `FileIDs[i], true, nil`; else return `0, false, nil` (not-found is NOT an error).
- Errors (`err != nil`) are reserved for genuine I/O/decode failures (corrupt node,
  short read, seek/read syscall failure) — never for the "well-defined not-found"
  case, matching the acceptance criterion's phrasing.

## Decision: test-scaffolding tree-builder

- Lives in `engine/btree/lookup_test.go` (not a separate file, since it's small and
  only Lookup's tests use it), named distinctly (`buildTestTree`) and given a
  prominent doc comment marking it as **test-only scaffolding for exercising Lookup,
  NOT the real insert API** that 1.2.3 will deliver (no auto-splitting, no rebalancing,
  caller must hand-construct correctly-shaped/sorted nodes).
- Builds a 3-level tree (root internal -> 2 internal children -> 4 leaves) over a
  fixed set of known paths, writes every node via `NodeStore.WriteNode`, returns the
  `*NodeStore` + root node ID for the test to call `Lookup` against.
