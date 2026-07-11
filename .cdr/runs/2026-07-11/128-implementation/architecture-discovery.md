# Architecture discovery — 4.5.12.4 NodeAllocator durability check

## Index-first pass

- `.cdr/index/file.jsonl`: `engine/catalog/idalloc.go` tagged
  `fileid-allocator, monotonic-counter, durable-sidecar-file` — the reference
  pattern this subtask mirrors.
- `.cdr/index/regression.jsonl`:
  - subtask "1.1.4" entry: flagged the *original* version of this gap for
    catalog's `IDAllocator` (no cross-check between sidecar high-water-mark
    and catalog.dat's actual max FileID). That gap was later closed inside
    `engine/catalog/idalloc.go` (`maxFileIDInCatalog` + the check in
    `NewIDAllocator`) — already-shipped code I read directly.
  - subtask "1.2.3" entry: explicitly says NodeAllocator (insert.go)
    "reintroduces the same sidecar-state-file-loss/ID-reuse residual risk...
    consider ... cross-checking against the highest node ID actually
    present". This is precisely issue #50/4.5.12.4 — same gap, later formal
    subtask.
  - subtask "4.5.12.3" entry (2026-07-11, commit d747925): unrelated
    (Lookup routing coverage), confirms lookup_test.go already has fresh
    content this run must not touch/duplicate.

## HLD/LLD pass

- `docs/HLD.md`: no direct mentions of NodeAllocator/idalloc; storage-engine
  durability principles only stated at a high level (WAL, checkpointing).
  Nothing here constrains this subtask further.
- `docs/LLD/btree.md`: describes NodeStore/NodeAllocator/SaveRoot-LoadRoot
  sidecar-file design at a summary level, references crabbing/latch
  concurrency model. Does not yet document the catalog-parity cross-check
  (this is a documented "known gap" candidate for the separate 4.5.12.7
  LLD-sync subtask, not this one — scope stays to insert.go/insert_test.go
  per the issue's own "Impacted modules" list).

## Source pass (read only after index/docs exhausted)

- `engine/catalog/idalloc.go` (reference implementation, read in full):
  `NewIDAllocator` opens/creates the `.idalloc` sidecar, restores `next` from
  it, then calls `maxFileIDInCatalog(fm)` which scans every catalog page
  (`fm.highestAllocated`, tracked in-memory by `FileManager`) and decodes each
  live record's `FileID` field to find the true on-disk max. If that max
  exceeds the sidecar's restored `next`, `NewIDAllocator` returns a
  descriptive error rather than proceeding.
- `engine/btree/insert.go` `NodeAllocator`/`NewNodeAllocator` (read in full):
  same sidecar-file shape (`.nodealloc`, single little-endian uint64), but
  `NewNodeAllocator` currently does zero cross-checking against the actual
  index file content before returning.
- `engine/btree/lookup.go` `NodeStore` (read in full): unlike
  `catalog.FileManager`, `NodeStore` has **no** free-list/allocator or
  in-memory "highest allocated" bookkeeping of its own (its own doc comment
  says so explicitly — allocator responsibility was deliberately deferred to
  whichever later subtask needed one, i.e. this package's own
  `NodeAllocator`). Nodes also carry no embedded self-identifying "my node
  ID" field inside their encoded content (leaf/internal node encodings only
  store keys/children/fileIDs/sibling pointers — confirmed via `node.go`'s
  encode/decode). So there is no `maxFileIDInCatalog`-style "decode every
  record and read back its own ID" option available here.
  - What NodeStore *does* guarantee: node ID N is always written at exact
    byte offset `N * NodeSize` (`ReadNode`/`WriteNode`), and node IDs are
    allocated strictly monotonically with no reuse and no gaps (every
    `Next()` call hands out `next+1` and nothing else ever calls `WriteNode`
    at an ID it didn't just receive from `Next()` — confirmed by grepping
    `WriteNode(` call sites: only `writeLeaf`/`writeInternal` helpers in
    insert.go/delete.go, always fed a freshly-allocated or
    previously-returned ID). So the file's on-disk size is a reliable proxy
    for "the highest node ID ever durably written": since node IDs are
    handed out 1, 2, 3, ... with no gaps, and each `WriteNode` call extends
    the file (via `os.File.WriteAt`'s implicit-grow-on-write-past-EOF
    behavior) to at least `(nodeID+1) * NodeSize` bytes, `fileSize/NodeSize -
    1` is exactly the highest node ID that has ever actually been written,
    for any file produced by this package's own allocator+WriteNode pair.
  - This is the same "derive the check from what the storage layer already
    durably guarantees" spirit as `maxFileIDInCatalog`, just adapted to
    NodeStore's simpler (non-catalog, no free-list) file layout: instead of
    decoding record content, we use file-size-as-write-high-water-mark.

## Design decision (revised mid-implementation -- see note below)

Add `maxNodeIDInStore(store *NodeStore) (uint64, error)` (new, unexported
helper in insert.go, alongside `NodeAllocator`) that `Stat`s
`store.f`, computes `size/NodeSize`, and returns `size/NodeSize - 1` (or `0`
if the file is smaller than 2 node-slots, matching reservedNodeID's "no nodes
yet" convention).

**Revision note (discovered during implementation, not anticipated at
planning time):** the first attempt called `maxNodeIDInStore` eagerly inside
`NewNodeAllocator` itself (construction time), mirroring
`engine/catalog/idalloc.go`'s `NewIDAllocator`/`maxFileIDInCatalog` exactly.
Running the full `engine/btree` suite against that version broke
`TestLookupInternalNodeMultiKeyRouting` (subtask 4.5.12.3, already merged as
commit d747925): that test hand-writes fixture nodes directly via
`store.WriteNode` (bypassing the allocator entirely -- a documented,
intentional test-only pattern, see `NodeStore`'s and `buildTestTree`'s doc
comments in lookup.go/lookup_test.go), then constructs a fresh
`NewNodeAllocator` afterward purely to exercise `Tree.Lookup` (never calling
`Next()`). At the NodeStore level this is indistinguishable from a genuinely
lost/stale sidecar (both look like "sidecar next=0, but real node content
already present on disk"), so an eager check necessarily rejects it too --
this is a hard regression against already-verified 4.5.12.3 work, and
`lookup_test.go` is explicitly out of this subtask's scope (and, per the
dispatching agent's instructions, must not be touched/regressed).

Resolution: the cross-check was moved from `NewNodeAllocator` (construction
time) to `Next()` (allocation time) -- the actual moment reissuing an
already-used ID would cause real corruption. `NodeAllocator` gained a new
`store *NodeStore` field (set by `NewNodeAllocator`) so `Next()` can call
`maxNodeIDInStore(a.store)` before persisting each candidate ID, erroring if
`candidate <= maxPresent`. This still fully closes the actual gap (silent ID
reuse), runs on every `Next()` call (not just the first), and never affects
any caller that only reads via `Lookup`/`Tree.Lookup` without ever calling
`Next()` -- which is exactly `TestLookupInternalNodeMultiKeyRouting`'s usage,
so it is completely unaffected by this change. Confirmed via full
`go test ./engine/btree/...` and `-race` runs, both green, with
`lookup_test.go` left byte-for-byte untouched (`git diff --stat` confirms).

This remains purely additive to `NodeAllocator`/`NewNodeAllocator`/`Next()` —
no changes to `Close()`, `Insert`, `propagate`, or any other insert.go entry
point, and no changes to `Tree.Insert`/`propagate`'s auto-`SaveRoot` logic
added by issue #41's bc08c0a (different sidecar, different struct).

## Impacted modules (matches issue's own list)

- `engine/btree/insert.go` (production change)
- `engine/btree/insert_test.go` (new test, appended, own doc-comment header,
  clearly scoped so 4.5.12.6's later SaveRoot-absent test can append after
  it without collision)
