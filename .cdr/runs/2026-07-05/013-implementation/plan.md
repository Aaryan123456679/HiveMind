# Plan — engine/catalog/catalog_bench_test.go

1. Test-local baseline type `globalLockCatalog` (unexported, benchmark-only):
   - fields: `fm *FileManager`, `mu sync.Mutex`, `index map[uint64]location`,
     `activePageID uint64`.
   - `newGlobalLockCatalog(fm *FileManager) *globalLockCatalog`.
   - `Put(rec CatalogRecord) error`: Encode outside the lock (mirrors real
     Catalog.Put), then take `mu` for the ENTIRE remainder: old-location lookup,
     tombstone-if-exists, insert (reusing/advancing activePageID exactly like
     Catalog.insert but without pageStripes -- mu already serializes all page
     I/O), and index update.
   - `Get(fileID uint64) (CatalogRecord, error)`: take `mu` for lookup + ReadPage +
     Decode.
   - Internal helpers `tombstoneLocked`/`insertLocked`/`tryInsertIntoLocked`
     mirror catalog.go's tombstone/insert/tryInsertInto logic 1:1 minus the
     pageStripes/activeMu granularity (folded into the single `mu`).

2. Shared benchmark helpers:
   - `benchNumFileIDs = 4096` (comparable order of magnitude to 2a.3.1's 2000
     fileIDs, spread across all 256 stripes).
   - `benchRecordFor(fileID uint64) CatalogRecord` builds a minimal valid record.
   - `setupStripedBenchCatalog(b *testing.B) *Catalog` / 
     `setupGlobalLockBenchCatalog(b *testing.B) *globalLockCatalog`: each opens
     its own FileManager on `b.TempDir()`.

3. `BenchmarkStripedVsGlobalLock(b *testing.B)`:
   - `b.Run("Striped", func(b *testing.B) {...})`: build real `*Catalog`,
     `b.ReportAllocs()`, `b.ResetTimer()`, `b.RunParallel(func(pb *testing.PB) {...})`
     where each iteration picks the next fileID via a shared `atomic.Uint64`
     counter mod `benchNumFileIDs`, and calls `Put` then `Get` on it.
   - `b.Run("GlobalLock", ...)`: identical structure against `globalLockCatalog`.
   - Running `-bench BenchmarkStripedVsGlobalLock` matches both subtests (Go's
     bench name matching is a substring/regex match on the full "Name/Sub" path),
     satisfying the test spec while producing side-by-side reported lines.

4. Verify direction: run locally with `-benchmem -run ^$`; expect Striped's
   ns/op to be meaningfully lower (and/or effective ops/sec higher) than
   GlobalLock's under `-cpu` > 1 parallelism, since GlobalLock forces full
   serialization of all fileIDs' Put/Get behind one mutex while Striped only
   serializes same-stripe/same-page operations. If not, increase GOMAXPROCS
   usage via b.SetParallelism or investigate page-stripe contention as the
   confound (e.g. too few distinct pages) and adjust benchNumFileIDs / record
   size assumptions.
