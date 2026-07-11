# Requirement — Subtask 4.5.19.1 (issue #58)

**Title (LOW):** Fix `idalloc.go`'s `maxFileIDInCatalog` to respect the free-list bitmap.

## Source

Recorded finding `.cdr/index/regression.jsonl` id
`hivemind-idalloc-maxfileid-ignores-freelist-bitmap` (surfaced during
`087-verification` for subtask 4.5.5.2 / issue #42, but never fixed — status
`open`, severity `low`, `currently_inert: true`). Cross-referenced against
`.cdr/memory/pending.md` reconciliation-audit entry (2026-07-11, pre-task
#20/#21 pass) which recommends fixing this *before* any future subtask wires
`FileManager.FreePage` into the Catalog CRUD/delete path.

## Defect

`maxFileIDInCatalog` in `engine/catalog/idalloc.go` scans every page ID from
`1` to `fm.highestAllocated` and calls `fm.ReadPage(pageID)` unconditionally,
without first checking `fm.isUsed(pageID)` (the free-list bitmap bit for that
page). `FileManager.FreePage` only clears the bitmap bit for a freed page —
it never zeroes or tombstones the page's on-disk bytes. Consequently, a freed
page can still contain a stale `CatalogRecord{FileID: N}` in one of its slots,
and `maxFileIDInCatalog` will still count that stale `N` toward the returned
maximum, even though the page is no longer live from the free-list's point of
view.

This exact defect pattern (trusting page content instead of consulting the
free-list bitmap first) was already identified and fixed at the
sidecar/node-allocator layer in commits `75203e0` (Catalog IDAllocator
cross-check) and `c2b1dc4` (NodeAllocator cross-check, `engine/btree/insert.go`)
during resolution of subtask 4.5.5.2 / issue #42's own defect. This subtask
applies the analogous "consult the free-list before trusting a page as live"
fix directly to `maxFileIDInCatalog` itself.

Currently inert in production: nothing in the Catalog CRUD path calls
`FileManager.FreePage` yet, so no real freed page with a stale record exists
today. The fix is preventative, closing the gap before it can be exploited by
a future subtask.

## Acceptance Criteria

1. `maxFileIDInCatalog` must skip any page ID in `[1, highestAllocated]` that
   is not marked used in the free-list bitmap (i.e. `fm.isUsed(pageID) ==
   false`), mirroring the fix pattern from `75203e0`/`c2b1dc4` (bitmap
   consulted before treating a page's content as live).
2. Add a regression test in `engine/catalog/idalloc_test.go` that:
   - Allocates several pages, writes a `CatalogRecord` with a high `FileID`
     into a high-numbered page.
   - Frees that page via `FileManager.FreePage` (bitmap bit cleared, bytes
     left stale/untouched, matching `FreePage`'s documented behavior).
   - Calls `maxFileIDInCatalog` and asserts it does **not** scan/read the
     freed page's stale record and returns the correct max among only the
     still-used pages.
3. Existing test spec passes: `go test ./engine/catalog/... -run
   TestMaxFileIDInCatalog -v`.
4. No exported signatures change; `NewIDAllocator`'s existing cross-check
   behavior (added in `75203e0`) continues to work unchanged for the normal
   (no frees) case.
