# Plan — task-1.5.1

## Package placement
`engine/` module root (`engine/go.mod`) currently has no non-test `.go` files —
only subpackage directories (btree, catalog, graph, loadtest, mvcc, rpc,
split, wal). A directory containing only `*_test.go` files is a legal Go
package, so `engine/integration_test.go` uses `package engine_test` and
imports the four subpackages by their full module path
(`github.com/Aaryan123456679/HiveMind/engine/{catalog,btree,wal}`).

## Composition model (confirmed from source, not just docs)
- `catalog.Open(path)` -> `*FileManager` (owns catalog.dat + free-list).
- `catalog.NewCatalog(fm)` -> `*Catalog` (fileID -> CatalogRecord, in-memory
  index built from Put calls only this process; no disk rescan).
- `catalog.NewIDAllocator(fm)` -> monotonic fileID source, `Next()` -> uint64
  starting at 1 (0 == InvalidFileID).
- `wal.OpenWriter(dir, maxSegmentBytes)` -> `*wal.Writer`, shared by both
  ContentStore (catalog Put/Delete records) and, in this test, also used to
  WAL-log B+Tree inserts via `wal.NewBTreeInsertRecord` +
  `wal.AppendAndApply`, mirroring the WAL-before-apply pattern content.go
  already uses for catalog mutations (docs/LLD/wal.md's "every mutation to
  the catalog or any index must be logged in the WAL before it is applied").
- `catalog.OpenContentStore(root, cat, w)` -> `*ContentStore`: `Create(rec,
  data)`, `Read(fileID)`, `Append(fileID, data) (thresholdCrossed bool, err)`.
- `btree.OpenIndexFile(path)` -> `*os.File`; `btree.NewNodeStore(f)` ->
  `*NodeStore`; `btree.NewNodeAllocator(store)` -> `*NodeAllocator`;
  `btree.Insert(store, alloc, rootNodeID, path, fileID) (newRootNodeID,
  err)` (rootNodeID starts at `reservedNodeID`==0 for an empty tree, handled
  internally by Insert's bootstrap branch); `btree.Lookup(store, rootNodeID,
  path) (fileID, found, err)`; `btree.PrefixScan(store, rootNodeID, prefix)
  ([]ScanEntry, error)` — NOTE: PrefixScan does NOT special-case an empty
  tree (rootNodeID == reservedNodeID); it is only called in this test after
  at least one Insert has happened, so this is never hit.

Per AGENT.md's table: btree stores topic-path -> fileID; catalog stores
fileID -> {location/size/version metadata}; ContentStore stores fileID ->
actual .md bytes. This test wires exactly that: for each topic path, allocate
a fileID (IDAllocator), Create its content+catalog record (ContentStore),
then Insert path->fileID into the B+Tree (WAL-logged), then verifies
lookup/prefix-scan/read consistency across all four.

## Test steps (`TestStorageCoreIntegration`)
1. One temp dir; open FileManager, Catalog, IDAllocator, wal.Writer,
   ContentStore, btree index file + NodeStore + NodeAllocator. Single
   goroutine throughout (no concurrency).
2. Create N (8) topic files under two topic prefixes (e.g.
   `topics/alpha/fileN`, `topics/beta/fileN`), each with distinct initial
   content: allocate fileID, ContentStore.Create(rec, data), then
   WAL-log+Insert path->fileID into the btree (root ID threaded through).
3. Append additional bytes to a subset of the created files via
   ContentStore.Append; capture thresholdCrossed for one file whose content
   is engineered to cross defaultSplitThresholdBytes (8KiB) to assert the
   documented signal semantics end-to-end (not just append correctness).
4. PrefixScan for `topics/alpha` and `topics/beta`: assert the returned
   (path, fileID) set exactly matches what was inserted under that prefix,
   in sorted order.
5. For every ScanEntry resolved from the btree, cross-check: Catalog.Get
   returns a record whose SizeBytes matches len(final content), and
   ContentStore.Read returns bytes byte-for-byte equal to what was
   written/appended.
6. Point Lookup for every inserted path (not just via scan) as an
   independent cross-check that Lookup and PrefixScan agree.
7. Negative check: PrefixScan for a nonexistent prefix returns empty/nil,
   Lookup for a nonexistent path returns found=false.

## Scope guard
No new production code. If any acceptance criterion cannot be met with the
actual btree/catalog/wal/content APIs as they exist, that gap will be
recorded here/handoff rather than worked around. In practice all needed
primitives (Insert/Lookup/PrefixScan, Create/Append/Read, IDAllocator,
wal.OpenWriter+AppendAndApply) already exist and compose cleanly; no gap
found.
