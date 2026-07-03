# Architecture Discovery — Subtask 1.1.2

## Existing state of `engine/catalog/`
- `doc.go` — placeholder package doc only.
- `record.go` / `record_test.go` (subtask 1.1.1, `verified`, commit `f974b495a53f41262f4b4963766c19c94cbfba76`):
  defines `CatalogRecord`, `RecordStatus`, `MaxRedirectTargets`, `RecordEncodedSize`,
  `Encode()`/`Decode()`. Fixed-size little-endian binary layout, hard errors (not
  panics/truncation) on invalid input — this is the precedent this subtask must follow
  for its own overflow-rejection requirement.
- No `page.go` yet — this subtask creates it from scratch.

## Relevant docs
- `docs/HLD.md`: `catalog/` = "On-disk metadata catalog, slotted 4KB pages,
  striped-mutex concurrency" (striped-mutex is out of scope here, lands in 1.1.5 /
  Epic 2A).
- `docs/LLD/catalog.md`: "Slotted 4KB pages (Postgres/SQLite-style layout), stored at
  `.meta/catalog.dat`", "free-list page for reclaiming deleted/merged slots" (free-list
  page itself is subtask 1.1.3, out of scope here — this subtask only needs
  page-internal free-space tracking, not a catalog-wide free-list).

## Design decision for this subtask

Classic slotted-page layout, single `[PageSize]byte` array per `Page`:

```
[ page header (fixed size) ]
[ slot directory, grows downward from just after header ]
[ ... free space ... ]
[ record/tuple data, grows upward from end of page ]
```

- Page header: `slotCount uint16`, `freeStart uint16` (offset just past the last slot
  directory entry — i.e. where the next new slot directory entry would go),
  `freeEnd uint16` (offset of the start of the lowest-allocated tuple region — i.e.
  where the next tuple's data should be placed, growing downward from PageSize).
  Free space = `freeEnd - freeStart`.
- Slot directory entry (fixed 8 bytes): `offset uint16`, `length uint16`, `deleted
  bool` (as uint16 flag or separate byte) — chosen as `offset uint16, length uint16,
  flags uint16, reserved uint16` for alignment simplicity (8 bytes/slot).
- `InsertSlot`: if an existing deleted slot's directory entry has enough capacity
  (`length >= len(data)`), it is reused in place (space-reuse without new tuple
  allocation and without needing a new directory entry) — this directly satisfies the
  "delete + reinsert reuses freed slot space" acceptance criterion. Otherwise a brand
  new slot entry + new tuple region is appended, only if `freeEnd - freeStart` has room
  for both the new 8-byte directory entry AND the tuple bytes.
- `ReadSlot`: bounds-check slotID, error if deleted or out of range, else return a copy
  of the tuple bytes.
- `DeleteSlot`: bounds-check slotID, mark directory entry deleted (tombstone), do NOT
  reclaim tuple-region bytes there (no compaction) — only its length is available for
  a future same-or-smaller insert to reuse in place (see InsertSlot reuse logic above).
- `FreeSpace() int`: exposes `freeEnd - freeStart` for allocator use (future 1.1.3) and
  internal overflow checks.

## No dependency added on `CatalogRecord`

Page stores/returns opaque `[]byte`. Higher layers (file manager, 1.1.3+) will call
`CatalogRecord.Encode()` before `InsertSlot`, and `Decode()` after `ReadSlot`. This
keeps `page.go` a clean, reusable, generic building block, consistent with existing
package separation.
