# Architecture Discovery — Subtask 4.5.19.1

## Index reading order followed

1. `.cdr/index/regression.jsonl` — finding
   `hivemind-idalloc-maxfileid-ignores-freelist-bitmap` (full text captured in
   `_regression-entry.txt` in this run dir).
2. `.cdr/memory/pending.md` — reconciliation-audit section
   ("Reconciliation audit findings (2026-07-11 ...)") confirms this is a known,
   recorded, not-yet-fixed low-severity/inert gap, and explicitly recommends
   fixing it before any future subtask wires page-freeing into Catalog CRUD.
3. `docs/LLD/catalog.md` — "Free-list" and "ID allocation (`idalloc.go`)"
   sections: confirms free-list is a page-0 bitmap (bit `i` ⇒ page `i+1`
   used/free), `FreePage` only flips a bitmap bit (no content
   zeroing/tombstoning), and `maxFileIDInCatalog` is documented as scanning
   "every currently-allocated page" — the LLD's own wording implies it should
   only consider pages the free-list still considers allocated/used, which is
   exactly the gap being closed.
4. `docs/HLD.md` — no idalloc-specific detail beyond pointing at
   `docs/LLD/catalog.md`; no HLD changes needed for this fix (internal,
   non-behavioral for the exported surface).

## Touched files (read directly, after indexes)

- `engine/catalog/idalloc.go` — `maxFileIDInCatalog` (lines ~134-184 before
  fix): loops `pageID := 1; pageID <= highest; pageID++`, calls
  `fm.ReadPage(pageID)` unconditionally, no `fm.isUsed` check.
- `engine/catalog/file.go`:
  - `isUsed(pageID uint64) bool` (line ~318): reads `fm.bitmap` bit for
    `pageID`. NOT internally synchronized — caller must hold `fm.mu` (same
    convention as `AllocatePage`/`FreePage`, which call it under `fm.mu.Lock()`).
  - `FreePage(pageID uint64) error` (line ~228): validates `pageID` is
    allocated and currently used, then `fm.setUsed(pageID, false)` and
    persists the bitmap — does NOT touch/zero the page's byte content. This
    confirms the defect: freed pages retain stale slot data indefinitely.
  - `highestAllocated uint64` field, guarded by `fm.mu`.
  - `ReadPage`/`WritePage`: perform their own I/O; not guarded by `fm.mu` for
    the actual disk read (per file.go's documented locking model — `mu`
    guards only bitmap/highestAllocated bookkeeping, not page I/O).

## Reference fix pattern (commits 75203e0, c2b1dc4)

- `75203e0` introduced `maxFileIDInCatalog` itself as part of adding a
  sidecar-vs-catalog cross-check to `NewIDAllocator` — it did not yet consult
  the free-list bitmap (that's this subtask's gap).
- `c2b1dc4` applied the analogous cross-check idea to `NodeAllocator`
  (`engine/btree/insert.go`), performing the "is this candidate ID already
  live on disk" check lazily, at usage time, using the node store's own
  existing decode/lookup primitives — the general pattern reinforced across
  both commits is: **before trusting any on-disk byte content as
  authoritative, first consult the authoritative liveness signal (bitmap /
  existing content) for that slot.** For `maxFileIDInCatalog`, the
  authoritative liveness signal for a whole page is `fm.isUsed(pageID)`.

## Design decision for this fix

Add a check `if !fm.isUsed(pageID) { continue }` inside the scan loop, before
calling `fm.ReadPage(pageID)`, using the already-existing unexported
`fm.isUsed` bitmap helper (same package, no new API needed). Since `isUsed`
is not internally synchronized, take `fm.mu.Lock()` once, snapshot the set of
used page IDs (or just hold the lock across the isUsed checks only, doing the
actual `ReadPage` I/O outside the lock to match `file.go`'s stated locking
model of not holding `mu` across page I/O), then iterate.
