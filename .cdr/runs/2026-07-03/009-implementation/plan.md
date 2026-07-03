# Plan — Subtask 1.1.3

1. Implement `engine/catalog/file.go`:
   - Constants: `DefaultCatalogFileName = ".meta/catalog.dat"`, `freeListPageID = 0`,
     bitmap header layout (`highestAllocated uint64` at offset 0).
   - `type FileManager struct { file *os.File; mu-free (no locking, single-threaded);
     bitmap [PageSize]byte; highestAllocated uint64 }`.
   - `Open(path string) (*FileManager, error)`:
     - `os.OpenFile(path, O_RDWR|O_CREATE, 0644)`.
     - `Stat` the file: if size == 0 (freshly created), initialize a new zeroed bitmap
       page, set `highestAllocated = 0`, persist it (WriteAt + Sync).
     - Else (existing file), `ReadAt` page 0 into the in-memory bitmap buffer and
       decode `highestAllocated` from the header.
     - Validate file size is a multiple of PageSize (sanity check); return error if
       not (corrupt file).
   - `Close() error` — closes underlying `*os.File`.
   - `AllocatePage() (uint64, error)`:
     - Scan bitmap bits 0..highestAllocated-1 (i.e. candidate page IDs 1..highestAllocated)
       for first free bit -> reuse.
     - If none free, `highestAllocated++`, zero-fill new page via WriteAt, mark bit
       used.
     - Persist bitmap (WriteAt + Sync). Return page ID.
   - `FreePage(pageID uint64) error`:
     - Reject pageID == 0 or pageID > highestAllocated.
     - Clear bit, persist bitmap (WriteAt + Sync).
   - `ReadPage(pageID uint64) (*Page, error)` / `WritePage(pageID uint64, p *Page) error`:
     - Reject pageID == 0 (reserved for bitmap).
     - Reject pageID > highestAllocated.
     - `ReadAt`/`WriteAt` at offset `pageID * PageSize`; WritePage also Syncs.
   - Bit helpers: `bitIndex(pageID) = pageID - 1`; `isUsed`/`setUsed`/`setFree` operating
     on the bitmap byte slice (bitmap bytes start right after the 8-byte header).

2. Implement `engine/catalog/file_test.go` with `TestCatalogFileManager` exercising,
   in order, the 5 scenarios from the test spec (see validation-matrix.json for the
   exact mapping):
   1. Open non-existent path (in `t.TempDir()`) -> file created, free-list initialized.
   2. Allocate N=10 pages -> distinct IDs, all marked used.
   3. Free a subset (simulate delete/merge) -> confirmed free via re-allocation probe
      or an internal accessor.
   4. Allocate again -> freed IDs are reused (assert returned IDs are a subset of the
      previously-freed set, not brand-new IDs beyond the prior high-water mark).
   5. Close and re-`Open` a second `FileManager` on the same path -> assert same
      used/free state observed (durability).

3. Run `go build ./engine/...`, then the targeted `-run TestCatalogFileManager -race
   -v` test, then the full `./engine/catalog/... -race -v` package test suite to
   confirm no regressions in record_test.go / page_test.go.

4. Write self-consistency.json, commit (Problem/Solution/Impact style, no push),
   handoff.json, update file.jsonl (2 entries) and task.jsonl (task-1.1.3 ->
   implemented).
