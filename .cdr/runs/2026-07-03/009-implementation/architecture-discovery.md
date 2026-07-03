# Architecture Discovery — Subtask 1.1.3

## Reading order followed

1. `.cdr/memory/*` — all empty (state.md, decisions.md, pending.md, impact-map.md,
   regression-routes.md, timeline.md); no prior decisions recorded to reconcile with.
2. `docs/HLD.md` — system-level context: `catalog/` is the on-disk metadata store for
   topic files; coordinates with `mvcc/`, `split/`, `btree/`, `wal/`.
3. `docs/LLD/catalog.md` — storage layout: slotted 4KB pages, Postgres/SQLite-style,
   stored at `.meta/catalog.dat`, with a free-list page reclaiming deleted/merged
   slots. WAL logs catalog mutations before they are applied (future subtask).
4. `.cdr/index/file.jsonl` / `.cdr/index/task.jsonl` — confirms `task-1.1.1` and
   `task-1.1.2` are `verified`; `engine/catalog/record.go` and `engine/catalog/page.go`
   are the only prior source files in this module.
5. `engine/catalog/record.go` — `CatalogRecord` type, `RecordEncodedSize` constant
   (fixed-size encode/decode, little-endian). Not directly used by file.go in this
   subtask (that wiring is CRUD/1.1.5's job) but establishes the little-endian
   convention this subtask follows for the free-list page's on-disk encoding.
6. `engine/catalog/page.go` — `Page` type: fixed `PageSize = 4096`, slotted layout with
   header (`slotCount`, `freeStart`, `freeEnd`), slot directory, tuple region.
   `NewPage()`, `InsertSlot`, `ReadSlot`, `DeleteSlot`, `FreeSpace()`, `SlotCount()`.
   No internal locking (documented as single-threaded-safe only). This subtask's
   `FileManager` builds the file layer directly on top of the raw byte layout, not on
   `Page.InsertSlot`, because the free-list is a fixed bitmap/array, not a variable-
   length slotted record store — reusing `Page`'s slot machinery would add needless
   indirection for a fixed uint64-per-page-ID bitmap. This is a deliberate deviation
   worth noting for the verifier: the free-list page is a *raw 4096-byte block*
   interpreted as a bitmap, not a `Page` instance with slots.

## Design decision: free-list encoding

Chose a **bitmap** stored in a dedicated page (page 0) over a linked free-list of
page IDs, per the acceptance-criteria hint that a bitmap is simplest for a fixed
small page count:

- Page 0 is reserved as the free-list bitmap page and is never allocated as a data
  page.
- Bitmap capacity: `(PageSize - headerSize) * 8` bits, i.e. one bit per page ID.
  With `PageSize = 4096` and an 8-byte header, that's `4088 * 8 = 32,704` page IDs
  representable per bitmap page (~127MB of data pages) before a follow-up subtask
  would need bitmap-page-chaining. That is far beyond what's needed to satisfy this
  subtask's acceptance criteria/test spec (N=10 allocations) and is documented as a
  known future extension, not implemented here (avoids over-engineering beyond
  subtask scope).
- Bit `i` (0-indexed against page 0, i.e. bit 0 = page 1, since page 0 itself is
  reserved for the bitmap and is always considered "used"/unavailable) records
  whether page `(i+1)` is allocated (1) or free (0).
- Header on the bitmap page stores `highestAllocated` (uint64) — the highest page ID
  ever allocated (i.e. current file-extension high-water mark) so `Open` knows where
  the file currently ends without needing to `Stat` in the common path (though `Open`
  does verify file size independently as a sanity check).

## Design decision: allocation strategy

`AllocatePage`:
1. Scan the bitmap for the lowest-numbered free bit (giving free-list reuse
   preference over extending the file — satisfies "reuse before appending").
2. If found, mark it used, persist the bitmap page (`WriteAt` + `Sync`), return that
   page ID. No file extension needed since the page already exists on disk (it was
   zeroed when originally allocated/extended, or freed-but-still-present).
3. If not found, extend the file by one page (`highestAllocated + 1`), zero-fill it
   via `WriteAt` of a zeroed 4096-byte buffer at the new offset, mark the bit used,
   bump `highestAllocated`, persist bitmap page.

`FreePage(pageID)`:
- Clears the bit for `pageID` in the in-memory bitmap, persists the bitmap page.
- Does not physically truncate the file (pages may be non-contiguously freed);
  truncation/compaction is out of scope.
- Rejects `pageID == 0` (reserved) and out-of-range/never-allocated page IDs with an
  error.

## Durability

`FreePage`/`AllocatePage` both call a shared internal `persistBitmap()` that does
`file.WriteAt(bitmapBytes, 0)` followed by `file.Sync()`. This satisfies "the
free-list page write itself should be a real WriteAt/Sync ... not just kept in
memory" without requiring a WAL (deferred to `engine/wal/`).

`ReadPage`/`WritePage` operate on `*Page` at file offset `pageID * PageSize` (pages
>= 1; page 0 is reserved for the bitmap and is not a valid target for `ReadPage`/
`WritePage` — attempting so returns an error, keeping the reserved page boundary
consistent).

## No conflicts found

No existing `file.go` or `file_test.go`; no naming collisions with `record.go`/
`page.go` symbols. `FileManager` is a new exported type; internal free-list bitmap
helpers are unexported.
