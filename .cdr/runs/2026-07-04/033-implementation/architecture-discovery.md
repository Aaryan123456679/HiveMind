# Architecture Discovery — Subtask 1.2.6

## Read order followed
1. `.cdr/memory/*` — all empty scaffolds (decisions.md, impact-map.md, pending.md,
   regression-routes.md, state.md, timeline.md); nothing to reconcile.
2. `docs/HLD.md` — `engine/btree/` is the on-disk B+Tree mapping topic path -> fileID, at
   `index/name.idx`. No HLD change needed (this subtask is purely an additive persistence detail
   within the module, not a system-level architectural change).
3. `docs/LLD/btree.md` — still describes the module at "scaffold + operations" level (point
   lookup, prefix scan, insert, delete); does not yet mention node-ID/root persistence sidecar
   files. Not updated by this subtask (LLD updates are documentation-agent's job per this repo's
   CDR convention); noted for the LLD sync agent.
4. `.cdr/index/file.jsonl` — confirms current feature set per file: `insert.go` already has
   `durable-sidecar-file` (the `.nodealloc` allocator) as of run `023-implementation`. `scan.go`
   has `prefix-scan` as of `031-implementation`. No `btree_test.go` yet exists (this subtask
   creates it fresh, per the issue's literal "Impacted modules").
5. `.cdr/index/task.jsonl` — confirms `task-1.2.1` through `task-1.2.5` are all `state: verified`.
   `task-1.2.6` does not yet exist as an entry (this run adds it).
6. `.cdr/index/regression.jsonl` — the specific gap: `NodeAllocator`'s own doc comment (in
   `insert.go`, not a regression-log entry itself, but stated as "Known gap ... expected to be
   revisited by 1.2.5/1.2.6") plus `032-verification`'s (1.2.5) recommendation about EmptyTree
   handling in lookup/scan tests are the closest existing breadcrumbs; no regression entry
   explicitly titled "root ID never persisted" exists as a JSONL row, but the gap is stated
   directly in source (`insert.go` lines ~32-37) and cross-referenced by the task prompt.

## Source read in full
- `engine/btree/node.go` — `NodeSize` (4096), node header layout (type byte, uint16 key count,
  uint64 version at fixed offsets), `LeafNode`/`InternalNode` structs + `Encode`/`Decode`,
  `OpenIndexFile` (creates file with `O_RDWR|O_CREATE` if missing).
- `engine/btree/lookup.go` — `NodeStore` (thin wrapper over `*os.File`, addresses nodes by
  `nodeID * NodeSize` byte offset), `reservedNodeID = 0` sentinel (never a valid node, mirrors
  `noSibling`), `ReadNode`/`WriteNode`, `descendToLeaf` (shared traversal helper used by Lookup,
  Insert, PrefixScan), `Lookup`.
- `engine/btree/insert.go` — `NodeAllocator`: sidecar file `<index-path>.nodealloc`, fixed 8-byte
  little-endian uint64 high-water-mark, `NewNodeAllocator` restores state on open (0 size => fresh,
  8 bytes => restore, else error), `Next()` does WriteAt + Sync **before** advancing in-memory
  state and returning success (so a crash between candidate-write and return never hands out a
  colliding ID on reopen). `Insert` bootstraps an empty tree when `rootNodeID == reservedNodeID`,
  and returns `newRootNodeID` — but does NOT persist it anywhere; this is exactly the idiom to
  mirror for the new root-ID sidecar.
- `engine/btree/delete.go` — `Delete` similarly returns `newRootNodeID` without persisting it;
  same non-responsibility applies here.
- `engine/btree/scan.go` — `PrefixScan(store, rootNodeID, prefix)`: descends via
  `descendToLeaf`, then walks `NextLeaf` links, using `strings.HasPrefix` per key, early-exiting
  once a key no longer matches (relies on sorted-order invariant). Returns `[]ScanEntry{Path,
  FileID}`.
- `engine/btree/insert_test.go` — `newTestStoreAndAllocator(t)` opens a **`t.TempDir()`-scoped**
  fresh index file via `OpenIndexFile` + `NewNodeStore` + `NewNodeAllocator`, registers
  `t.Cleanup` to close both. This helper cannot be reused as-is for the persist/reload test because
  it does not expose the on-disk path (needed to reopen the *same* file after "closing"), and its
  `t.Cleanup`-based close happens only at test end, not mid-test. `TestPersistReload` needs its
  own local setup that keeps the path and closes explicitly mid-test to simulate a restart.
- `engine/btree/scan_test.go` — `buildScanTree` builds via real `Insert`; `sortedSubset` computes
  expected filtered/sorted results for comparison. `sprintfTopic(i)` generates deterministically
  sortable keys. Useful patterns to reuse in the new test file (will define locally to avoid
  cross-file test-only coupling, matching this codebase's existing pattern of each _test.go file
  being fairly self-contained with small helper duplication e.g. `genKey` also exists independently
  in insert_test.go).
- `engine/btree/delete_test.go` — confirms `Delete` signature `(store, alloc, rootNodeID, path)
  -> (newRootNodeID, found, err)` and existing structural-invariant-checking helper patterns
  (`assertNoOrphanedPointers`) — not required for 1.2.6's scope (round-trip of Insert-built tree
  is the literal test spec), but confirms Delete follows the identical "caller owns the returned
  root ID" pattern as Insert, so the new root-persistence design applies uniformly to both.

## Design decision: where SaveRoot is (and is NOT) called

- New file `engine/btree/persist.go` adds:
  - `rootStateSuffix = ".root"` sidecar suffix (mirrors `nodeAllocSuffix = ".nodealloc"` exactly).
  - `rootStateSize = 8` (single little-endian uint64).
  - `SaveRoot(store *NodeStore, rootNodeID uint64) error` — opens (or creates)
    `<index-path>.root`, WriteAt + Sync the 8-byte LE root node ID, closes the sidecar file handle
    it opened. Stateless across calls (unlike `NodeAllocator`, which holds a long-lived handle for
    performance across many `Next()` calls) because `SaveRoot` is expected to be called
    comparatively rarely (batch/checkpoint boundaries, not once per key), so the overhead of
    open+write+sync+close per call is acceptable and keeps the API simple (no separate
    constructor/Close lifecycle to manage).
  - `LoadRoot(store *NodeStore) (rootNodeID uint64, err error)` — opens `<index-path>.root`
    read-only; if it does not exist (`os.IsNotExist`), returns `(reservedNodeID, nil)` — consistent
    with `Insert`'s existing empty-tree bootstrap convention (fresh/never-persisted tree looks the
    same as an empty tree to a caller). If it exists but has an unexpected size, returns an error
    (mirrors `NewNodeAllocator`'s existing size-validation behavior). Otherwise decodes and returns
    the 8-byte LE uint64.
- **`SaveRoot` is NOT called inside `Insert` or `Delete`.** Both already return `newRootNodeID` to
  the caller on every call; wiring a persistence path into them would require threading an
  index-file path (or a `*NodeAllocator`-like sidecar handle) through every `Insert`/`Delete` call,
  and — more importantly — would force an `fsync` on every single insert/delete, which is a
  significant hot-path performance regression with no design discussion or acceptance criterion
  asking for it here. Ownership of *when* to durably commit a root pointer is a policy decision
  (e.g. checkpoint every N ops, on clean shutdown, or per-transaction once WAL/mvcc subtasks land)
  that belongs to a later, dedicated subtask/module (`wal/`, `mvcc/`, or a future
  `btree.Tree` convenience wrapper) — not to this one. This subtask only adds the mechanism
  (`SaveRoot`/`LoadRoot`) and demonstrates it end-to-end in `TestPersistReload`, where the test
  itself plays the role of "the caller who has decided to commit."
