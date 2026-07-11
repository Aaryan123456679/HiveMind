# Plan: Subtask 4.5.3.5

1. Refactor `newTestBtree` into `newTestBtreeWithPath` (returns `(*btree.Tree, string)`) plus a
   new `openTestBtreeAt(t, path, rootNodeID)` helper, so the on-disk index file path is
   recoverable and a fresh `*btree.Tree` can later be opened against the same path with an
   explicit `rootNodeID`. `newTestBtree` becomes a thin wrapper preserving all existing callers'
   behavior unchanged.
2. Fix `putSplittingRecord` (test helper used only by `TestSplitAtomicCommit`) to seed its
   `StatusSplitting` record via `wal.NewCatalogPutRecord` + `wal.AppendAndApply` wrapping
   `cat.Put`, mirroring production's `transitionStatus` — required so a freshly-reconstructed
   `*catalog.Catalog` (via `catalog.RecoverFromWAL`) can actually recover this seed record.
   Update its one call site to pass `w`.
3. Add `catalogPath`, `treeIndexPath`, `edgesDir` fields to `atomicCommitTestDeps`; populate
   them in `TestSplitAtomicCommit`'s `newDeps` closure (derived from `cs.ContentPath(0)` for
   `catalogPath`, from `newTestBtreeWithPath`'s return for `treeIndexPath`, and from
   `appenderDirs[appender]` for `edgesDir`).
4. Add `reopenFreshSplitDeps(t, deps, preRecoveryRoot)` helper: opens a new `*catalog.FileManager`
   + `*catalog.Catalog`, replays `catalog.RecoverFromWAL`; opens a new `*btree.Tree` via
   `openTestBtreeAt` rooted at `preRecoveryRoot`; opens a new `*graph.EdgeAppender` via
   `graph.OpenEdgeAppender` (registering it in `appenderDirs` for `readAppenderEdges`/
   `edgeCount` test helpers, with `t.Cleanup` unregistering it).
5. In `crashPointTest`, after the simulated-crash `ExecuteSplitAtomic` call and the existing
   pre-recovery status/guard assertions (unchanged, still against `deps`), capture
   `preRecoveryRoot := deps.tree.Root()` and call `reopenFreshSplitDeps` to get
   `freshCat, freshTree, freshAppender`. Build `freshDeps := deps` with those three fields
   overridden. Route both `RecoverSplitCommits` calls (initial + idempotency re-run) and every
   post-recovery assertion (`cat.Get`, `tree.Lookup`, `readAppenderEdges`, `edgeCount`,
   `assertFullSplitApplied`) through `freshDeps`/`freshCat`/`freshTree`/`freshAppender` instead
   of the original `deps.cat`/`deps.tree`/`deps.appender`.
6. Run `go vet ./engine/split/...` and `go test ./engine/split/... -race -run TestSplitAtomicCommit -v`,
   then the full package suite `go test ./engine/split/... -race`.
7. Self-consistency check (internal only, not verification): confirm build is green and every
   acceptance-criterion row in validation-matrix.json is covered by an actual test run.
8. One local commit, `engine/split/execute_test.go` only (explicit path, no `git add -A`).
9. Write handoff.json with pointers only.
