# Architecture Discovery

## Current FreePage implementation (engine/catalog/file.go:184-198)

```go
func (fm *FileManager) FreePage(pageID uint64) error {
	if pageID == freeListPageID {
		return fmt.Errorf("catalog: cannot free reserved free-list page %d", pageID)
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()

	if pageID == 0 || pageID > fm.highestAllocated {
		return fmt.Errorf("catalog: cannot free page %d: not an allocated page (highest allocated is %d)", pageID, fm.highestAllocated)
	}

	fm.setUsed(pageID, false)
	return fm.persistBitmapLocked()
}
```

Findings:
- `FreePage` currently validates that `pageID` is not the reserved free-list
  page (0) and that it is within `[1, highestAllocated]` (i.e. that it was
  ever extended into the file). It does NOT check whether the page's bitmap
  bit is currently 1 (used) before calling `setUsed(pageID, false)`.
- `setUsed(pageID, false)` unconditionally clears the bit and is idempotent
  at the bit level, so calling `FreePage` twice on the same, currently-free
  page silently succeeds both times (no error), re-persisting the same
  all-zero bit and re-touching `persistBitmapLocked` (an extra disk sync) but
  otherwise doing nothing observably wrong to the bitmap itself.
- The real danger this masks: a caller (e.g. a future split/mvcc bug) that
  frees a page it no longer owns without knowing another part of the code
  already freed and `AllocatePage()`-reused it. `AllocatePage` scans for the
  first bit that is 0 (free) and reuses it, marking it used again. If two
  callers both freed page N (double-free) and only one of them still holds a
  live reference, a legitimate FreePage(N) call from the buggy caller could
  race with an intervening reallocation and free a page that is now in
  active use by someone else — corrupting that other owner's data
  invisibly. Failing loudly on redundant frees (checking `isUsed` first) is
  the standard defense: it can't detect the corrupted-reuse case after the
  fact, but it does turn the "calls FreePage twice in a row on the same page
  ID it still (wrongly) thinks it owns" bug pattern into an immediate, loud
  error at the second call site, rather than a silent no-op that lets the
  bug ship.
- `isUsed(pageID)` (file.go:270-275) already exists as an unexported bitmap
  helper: `fm.bitmap[byteOff]&mask != 0`. It's exactly what's needed; no new
  state is required.

## Locking
- `fm.mu` already guards the bitmap and `highestAllocated`, and is already
  held across the existing validity checks + `setUsed` + `persistBitmapLocked`
  in `FreePage`. The new `isUsed` check fits naturally inside the same
  critical section, right after the existing range check and before
  `setUsed`.

## Test conventions (engine/catalog/file_test.go)
- Table/subtest style using `t.Run("description", func(t *testing.T) { ... })`
  within one `TestCatalogFileManager`, OR standalone `TestXxx` functions (see
  `TestCatalogFileManagerNarrowLockDoesNotSerializeAcrossIO`).
- Uses `t.TempDir()` + `filepath.Join(dir, "catalog.dat")`, never the
  `DefaultCatalogFileName` constant, per the doc comment on that constant.
- Error-path assertions use `if err := fm.FreePage(x); err == nil { t.Fatalf(...) }`.
- The test spec in the issue names a distinct top-level test function
  `TestFreePageDoubleFreeRejected`, so it will be added as its own standalone
  `func TestFreePageDoubleFreeRejected(t *testing.T)` rather than folded into
  the existing `TestCatalogFileManager` subtests, to match the exact
  `-run` pattern required by the acceptance test spec.

## Call-site survey (repo-wide grep, read-only)
`grep -rn "FreePage" --include="*.go" .` — the only non-comment call site of
`FreePage` in the entire repo is inside `engine/catalog/file_test.go` itself
(the existing test file). `engine/split`, `engine/mvcc`, `engine/btree`, and
`engine/wal` contain zero references to `FreePage` (grepped explicitly,
empty result). Conclusion: no existing caller anywhere in the codebase
relies on double-free being a silent no-op; it is safe to make the second
`FreePage` call on an already-free page return an error without breaking any
current caller. This is also called out explicitly in handoff.json per the
task's instructions, since the check was mandatory regardless of outcome.
