# Plan (2b.4.1)

1. `engine/catalog/content.go`:
   - Add `HeaderOffset{Header string; Offset int}` exported type.
   - Add `headerCache map[uint64][]HeaderOffset` + `headerCacheMu sync.Mutex`
     fields to `ContentStore`, initialized in `OpenContentStore`.
   - Add unexported `computeHeaderOffsets(content []byte) []HeaderOffset`:
     scans content line-by-line tracking byte offset, matches ATX markdown
     headers (`^#{1,6}(\s|$)`), records header offset + trimmed line text.
   - Add `(cs *ContentStore) ReadPartial(fileID uint64) ([]HeaderOffset, error)`:
     resolve fileID via cat.Get (ErrNotFound passthrough, matching Read/Append
     convention), take `cs.stripes[stripeFor(fileID)]` lock, check
     `headerCacheMu`-guarded cache, on miss read `cs.ContentPath(fileID)` and
     compute+store, return a defensive copy.
   - Add `(cs *ContentStore) InvalidateHeaderCache(fileID uint64)`: takes only
     `headerCacheMu`, deletes the entry. Safe to call from any package/lock
     context since it never touches `cs.stripes`.
   - In `Append`'s existing WAL apply closure, after `cs.cat.Put` succeeds,
     call `cs.headerCacheMu.Lock(); delete(cs.headerCache, fileID); cs.headerCacheMu.Unlock()`
     (inline, not via InvalidateHeaderCache, since Append already holds
     `cs.stripes[stripe]` and InvalidateHeaderCache's own lock is independent
     anyway -- calling the exported method directly is equally safe; use it
     for consistency/DRY).

2. `engine/split/execute.go`:
   - In `ExecuteSplitRedirectStub`'s apply closure, after `cat.Put(updated)`
     succeeds, call `cs.InvalidateHeaderCache(originalFileID)`.
   - In `ExecuteSplitAtomic`'s apply closure, after `cat.Put(updated)`
     succeeds, call `cs.InvalidateHeaderCache(originalFileID)`.

3. Tests:
   - `engine/catalog/content_test.go`: `TestReadPartialComputesHeaderOffsets`,
     `TestAppendInvalidatesHeaderCache` (populate cache via ReadPartial,
     Append, ReadPartial again, assert offsets reflect new content).
   - `engine/split/execute_test.go`: `TestSectionIndexInvalidation` per the
     issue's literal test spec -- run under `-race`: perform a split via
     `ExecuteSplitAtomic`, call `cs.ReadPartial` against the old (now stub)
     fileID and the new fileIDs, assert offsets reflect post-split content
     only (stub content's own headers/lack thereof for the old fileID; each
     new file's own header offsets for the new fileIDs).

4. Update `docs/LLD/catalog.md` / `docs/LLD/split.md`'s "Known risks" bullets
   only if their wording needs updating to reflect the risk now being
   mitigated -- read current wording first; likely leave as-is since the risk
   description remains accurate (describes the invariant the code now
   upholds), not a residual open risk. Decide during implementation.
