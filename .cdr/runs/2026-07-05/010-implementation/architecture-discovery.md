# Architecture discovery — task-2a.3.1

Read in full:
- `engine/catalog/catalog.go` (466 lines): `Catalog` struct with `stripes [256]sync.Mutex`
  keyed by `stripeFor(fileID) = fileID % numStripes`, `indexMu sync.RWMutex` guarding the
  `index map[uint64]location`, `pageStripes [256]sync.Mutex` keyed by pageID guarding the
  physical page read-modify-write sequence. `Put`/`Get`/`Delete`/
  `CompareAndSwapCurrentVersion` each take the record's stripe lock for the duration of
  their critical section. `stripeFor` is plain `fileID % numStripes` (not a hash) — fileIDs
  are monotonically allocated, so consecutive fileIDs land in consecutive/distinct stripes;
  `fileID` and `fileID + numStripes` collide into the SAME stripe (existing
  `TestCatalogStripesDoNotSerializeAcrossDifferentFileIDs` already exploits this fact to
  find a same-stripe fileID).
- `engine/catalog/catalog_test.go` (294 lines, existing): `newTestCatalog(t)` helper (fresh
  `t.TempDir()`-backed `FileManager` + `NewCatalog`), `testRecord(fileID)` helper building a
  `CatalogRecord` with `CurrentVersion: 1`, and an existing
  `TestCatalogConcurrentDistinctFileIDs` scenario (300 fileIDs, 4 workers per ID, identical
  Put per worker so any successful Get must equal `rec` exactly) which is the closest
  existing style precedent but does NOT implement a serial-execution oracle or varied
  per-fileID CRUD sequences (Put+Delete interleavings) — that is the gap 2a.3.1 fills.

No production code (`catalog.go`) changes are required or in scope; `stripeFor`,
`Put`/`Get`/`Delete` semantics are already correct per 2a.1/2a.2 verification. This subtask
is test-only, adding `TestStripedConcurrencyStress` to `catalog_test.go`.
