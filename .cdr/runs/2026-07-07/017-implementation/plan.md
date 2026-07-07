# Plan (subtask 2b.3.2)

1. In `engine/split/execute.go`, add:
   - A package-level stub-format constant `redirectStubHeader =
     "HIVEMIND-REDIRECT-STUB v1"`.
   - `buildRedirectStubContent(targetFileIDs []uint64) []byte` -- pure
     helper producing the deterministic stub bytes (header line, then one
     decimal fileID per line, in the given order).
   - `ExecuteSplitRedirectStub(cat *catalog.Catalog, w *wal.Writer, cs
     *catalog.ContentStore, originalFileID uint64, newFileIDs []uint64)
     (catalog.CatalogRecord, error)`:
     - Validate cat/w/cs non-nil, newFileIDs non-empty, len(newFileIDs) <=
       catalog.MaxRedirectTargets.
     - Read current record via `cat.Get(originalFileID)`; require
       `Status == catalog.StatusSplit` (mirrors orchestrate.go's
       `transitionStatus` precondition style); return a sentinel error
       (`ErrNotSplit`) otherwise, without writing anything.
     - Write stub content to `cs.ContentPath(originalFileID)` via the
       existing `writeNewContentFile` helper (already in execute.go from
       2b.3.1).
     - Build updated record: `Status = catalog.StatusRedirect`,
       `RedirectTargetIDs = newFileIDs` (copied defensively), leave other
       fields (FileID, PathHash, CurrentVersion, SizeBytes, ParentTopicID,
       LastModified) unchanged except update `SizeBytes` to the stub's
       length (record's own SizeBytes should reflect what's now physically
       on disk at that path) -- update `LastModified` too, matching
       `Append`'s convention of updating both alongside content changes.
     - Encode, WAL-log via `wal.NewCatalogPutRecord` +
       `wal.AppendAndApply`, apply via `cat.Put`, same WAL-before-apply
       idiom used throughout `orchestrate.go`/`content.go`.
     - Return updated record.
   - New sentinel error `ErrNotSplit` (`"split: execute: redirect stub:
     catalog record is not StatusSplit"`).

2. In `engine/split/execute_test.go`, add `TestSplitRedirectStub`:
   - Reuse `newTestContentStoreDeps` (extended to also return a `*wal.Writer`
     or reuse `newTestWAL` directly) to get idAlloc/cs/cat/w.
   - Create an original file's catalog record directly (`cs.Create` or
     `cat.Put` + manual content write) with `Status = StatusSplit` (simulate
     having already gone through `EndSplit(fileID, StatusSplit)`).
   - Run `ExecuteSplitAllocateAndWrite` against a fixture plan to get the
     `map[string]NewPath]fileID` from 2b.3.1.
   - Call `ExecuteSplitRedirectStub` with the resulting new fileIDs.
   - Assert: returned/re-fetched record has `Status == StatusRedirect`,
     `RedirectTargetIDs` equals (order-sensitive) the new fileIDs; stub file
     content at `cs.ContentPath(originalFileID)` matches
     `buildRedirectStubContent`.
   - Add negative subtests: nil cat/w/cs, empty newFileIDs, too many
     newFileIDs (> MaxRedirectTargets), record not found, record status !=
     StatusSplit (e.g. StatusActive) all return errors without mutating.

3. Self-consistency: build/vet/fmt, targeted test, full `./split/...` race
   suite, and (since catalog package itself is NOT modified) confirm no
   catalog changes are needed -- skip the "full catalog suite" run only if
   truly zero catalog/ files were touched; verify via `git diff --stat`
   before deciding.
