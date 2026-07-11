# Plan — Subtask 4.5.19.1

1. In `engine/catalog/idalloc.go`, modify `maxFileIDInCatalog`:
   - Take `fm.mu.Lock()`, snapshot `highest := fm.highestAllocated`, and build
     a `used` set (or just check `fm.isUsed(pageID)` per ID while still
     holding the lock, collecting the resulting bool slice/set), then
     `fm.mu.Unlock()`.
   - In the scan loop, before calling `fm.ReadPage(pageID)`, check the
     snapshotted "was used at snapshot time" result for `pageID`; if not
     used, `continue` (skip read entirely).
   - Update the function's doc comment to state it only considers pages
     currently marked used in the free-list bitmap.
2. In `engine/catalog/idalloc_test.go`, add a new test case (new `t.Run` or
   standalone `Test...` function per repo convention — check existing style
   first) that:
   - Builds a `FileManager` + allocates a few pages.
   - Writes a `CatalogRecord` with a high `FileID` (e.g. 9999) into one
     high-numbered page via `WritePage`.
   - Calls `fm.FreePage` on that page (bitmap bit cleared, bytes untouched).
   - Writes/keeps a lower `FileID` (e.g. 42) live in a still-used page.
   - Calls `maxFileIDInCatalog(fm)` and asserts the result is 42 (the freed
     page's stale 9999 must not be counted), and asserts no error.
3. Run `go build ./...` and `go test ./engine/catalog/... -run
   TestMaxFileIDInCatalog -v` plus full `go test ./engine/catalog/...` to
   confirm no regressions in sibling tests (e.g. `NewIDAllocator` cross-check
   tests from `75203e0`).
4. Self-consistency check, one commit (Problem/Solution/Impact), handoff.

## Non-goals

- Not wiring `FileManager.FreePage` into the Catalog CRUD/delete path (that's
  a separate, larger future subtask per `pending.md`'s own note).
- Not changing `FreePage`/`isUsed`/`AllocatePage` in `file.go`.
- No exported API/signature changes.
