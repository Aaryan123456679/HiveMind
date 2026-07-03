# Plan — Subtask 1.1.5

## Goal
Implement `Catalog` in `engine/catalog/catalog.go` exposing `Put`, `Get`, `Delete`
against `CatalogRecord`s stored via the existing `Page`/`FileManager` primitives, with
striped-mutex concurrency per `docs/LLD/catalog.md`.

## Steps
1. Define `ErrNotFound` sentinel and `location{pageID, slotID}` type.
2. Define `Catalog` struct: `fm *FileManager`, `stripes [256]sync.Mutex`,
   `index map[uint64]location` + `indexMu sync.RWMutex`, `activePageID uint64` +
   `activeMu sync.Mutex` for the shared new-insert page cursor, and (after discovering
   the FileManager concurrency gap during implementation) `fmMu sync.Mutex`
   serializing all calls into `fm`.
3. `NewCatalog(fm *FileManager) *Catalog` constructor. Document (and accept, as an
   explicitly out-of-scope known gap) that the index starts empty rather than being
   rebuilt from an on-disk scan, since `FileManager` exposes no page-enumeration API
   and adding one is out of this subtask's impacted-modules scope.
4. Implement `stripeFor(fileID) uint64` as `fileID % numStripes` (documented simple-
   modulo tradeoff vs a proper hash).
5. Implement `Put`: encode record, lock fileID's stripe, look up + tombstone any
   existing slot (delete-then-reinsert, documented simplicity tradeoff), insert new
   slot via the shared active-page cursor (allocating a new page via
   `FileManager.AllocatePage` when the active page lacks room), update the index,
   unlock.
6. Implement `Get`: lock fileID's stripe (for linearizability with a concurrent Put's
   delete-then-reinsert sequence), look up location under `indexMu` (RLock), read the
   page/slot, decode, unlock stripe.
7. Implement `Delete`: lock fileID's stripe, remove from index under `indexMu` (Lock),
   return `ErrNotFound` if absent, otherwise tombstone the slot, unlock.
8. Write `catalog_test.go` covering the 4 required scenarios (see validation-matrix.json)
   plus a stripeFor sanity check.
9. Run `go build ./engine/...`, `go vet ./engine/catalog/...`,
   `go test ./engine/catalog/... -run TestCatalog -race -v`.
10. Discovered (via `-race`) that FileManager's own internal state (highestAllocated,
    free-list bitmap) is not safe for concurrent multi-goroutine calls into it, despite
    per-page striping of Catalog's own locks. Revised design: replaced the initial
    per-page `pageStripes` approach with a single `fmMu` serializing all `fm.*` calls,
    documented as a distinct, necessary 3rd lock (see catalog.go's Catalog doc
    comment). Re-ran the full test suite to confirm the race is resolved and all
    scenarios still pass.
11. Run the full `engine/catalog` package test suite (`-race`) to confirm no
    regressions against the previously-verified record/page/file/idalloc tests.
12. Write CDR artifacts (this plan, validation-matrix, self-consistency, handoff),
    update `.cdr/index/file.jsonl` and `.cdr/index/task.jsonl`.
13. One local commit (Problem/Solution/Impact style), no push.

## Explicit non-goals (repeated from requirement.md)
- No MVCC versioning, no split logic, no WAL logging.
- No fix to the 1.1.4-flagged IDAllocator cross-check gap (documented only).
- No fix/change to `file.go`'s internal locking; instead Catalog compensates for the
  documented gap at its own layer (fmMu), matching file.go's own doc comment that says
  a future striped-mutex-CRUD subtask (this one) is "responsible for synchronizing
  concurrent access to a shared FileManager."
