# Architecture discovery

Read in full: engine/catalog/catalog.go (real Catalog), plus signatures from
file.go (Open, FileManager.AllocatePage/ReadPage/WritePage), record.go
(CatalogRecord, Encode/Decode), page.go (NewPage/InsertSlot/ReadSlot/DeleteSlot),
and catalog_test.go's newTestCatalog/testRecord helper patterns.

Key facts:
- Package is `catalog`; catalog_bench_test.go will be an INTERNAL test file
  (package catalog), so it can call unexported FileManager/Page methods directly,
  matching catalog_test.go's existing pattern.
- Catalog's locking model has 3 distinct locks: stripes[256]sync.Mutex (keyed by
  fileID % 256), indexMu sync.RWMutex (guards the index map), activeMu (guards the
  shared active-page cursor). Put/Get/Delete take the fileID's stripe lock for the
  whole read-modify-write, plus brief indexMu access, plus (via insert/tombstone)
  pageStripes[256] keyed by pageID.
- Open(path) creates a FileManager backed by a real file; tests use
  t.TempDir()/"catalog.dat" per catalog_test.go's newTestCatalog. Benchmarks need
  their own temp-dir-backed FileManager per sub-benchmark (b.TempDir()).
- No existing baseline/global-lock construct exists anywhere in the codebase;
  2a.3.1's stress test only exercises the real Catalog.

Baseline construction decision: build a small unexported `globalLockCatalog` type,
local to catalog_bench_test.go only (test-only, never referenced by production
code), that wraps the SAME *FileManager and reuses Page/CatalogRecord primitives,
but replaces stripes+indexMu+activeMu+pageStripes with a single sync.Mutex
guarding the entire Put/Get critical section (map lookup + page I/O + active-page
bookkeeping) end to end. This keeps the underlying storage/I/O work identical
between the two variants being benchmarked, isolating the locking-strategy
difference as the variable under test -- the fairest possible comparison per the
subtask's own guidance.
