# Architecture Discovery: Subtask 4.5.3.5

## Index lookups (before source)

- `.cdr/index/file.jsonl`: `docs/LLD/split.md` (module engine/split, feature "lld"),
  `engine/catalog/content.go` notes "wal-before-apply-content" idiom.
- `.cdr/index/task.jsonl`:
  - `task-2b.3.6` (verified): introduced `ExecuteSplitAtomic`'s WAL-covered commit path and
    `RecoverSplitCommits` crash-recovery replay.
  - `task-2b.5.1` (verified): "real production bug found+fixed (ExecuteSplitAtomic/
    RecoverSplitCommits never created catalog records for split-off fileIDs)" — confirms this
    exact test (`TestSplitAtomicCommit`'s crash-injection subtests) has already caught a real
    bug once before; strengthens the case that faithful-to-restart recovery testing here is
    high-value, not cosmetic.
- No pre-existing LLD section specifically prescribes crash-injection test methodology; this
  is a test-fidelity subtask, not a behavior/API change.

## Key production invariants discovered (read after indexes, targeted to touched area)

- `engine/catalog/catalog.go`'s `Catalog` doc comment: the `fileID -> location` index is
  **entirely in-memory** and **process-lifetime-scoped**; `NewCatalog` always starts with an
  empty index even against an existing `catalog.dat`. Underlying page bytes are durably
  persisted regardless. `catalog.RecoverFromWAL(cat, walDir)` is the documented mechanism to
  rebuild the index after a restart, by replaying `RecordCatalogPut`/`RecordCatalogDelete` WAL
  records via `cat.Put`/`cat.Delete`.
- `engine/split/execute.go`'s `RecoverSplitCommits` doc comment explicitly states it "does not
  itself reconstruct `*btree.Tree`'s root pointer or `FileGuard`'s in-memory state" — those are
  documented as deliberately out of scope for `RecoverSplitCommits` itself, left to callers.
- `engine/btree`: `Tree` keeps its root node ID (`root` field) purely in-memory; node *pages*
  are durably persisted via `NodeStore`/`NodeAllocator` writing to the on-disk index file, but
  nothing persists the root pointer itself. `btree.NewTree(store, alloc, rootNodeID)` requires
  the caller to supply whatever root is currently known-good.
- `engine/graph/edge_append.go`'s `EdgeAppender`: append-only WAL-backed log; `ReadAll(dir)`
  re-derives all edges directly from on-disk log contents on every call (used internally by
  `AppendEdgeIfAbsent`) — reopening via `graph.OpenEdgeAppender(dir)` needs no separate replay
  step, unlike `Catalog`.
- `engine/split/orchestrate.go`'s `transitionStatus` (production `BeginSplit`/`EndSplit` path)
  always writes catalog Status transitions through `wal.NewCatalogPutRecord` +
  `wal.AppendAndApply` before `cat.Put` — i.e. in production, a `StatusSplitting` record is
  always WAL-covered.

## Gap found in the existing test fixture

`engine/split/execute_test.go`'s `putSplittingRecord` test helper (used only by
`TestSplitAtomicCommit`'s `newDeps`) seeded the pre-split `StatusSplitting` catalog record via
a **bare `cat.Put`**, bypassing the WAL entirely — unlike production's `transitionStatus`. This
is a hidden precondition for 4.5.3.5: reconstructing a genuinely fresh `*catalog.Catalog` via
`catalog.RecoverFromWAL` after the simulated crash would NOT recover this seed record (since it
was never logged to the WAL in the first place), causing every crash-point subtest to spuriously
fail post-reconstruction with `ErrNotFound` for `originalFileID`. Fixed as part of this subtask
(see plan.md) — required for 4.5.3.5's acceptance criteria to be satisfiable at all, not an
independent scope expansion.

## Files read (source, only after index/doc discovery above)

- `engine/split/execute_test.go` (full file, in particular: `newTestContentStoreDepsWithWAL`,
  `newTestBtree`, `newTestEdgeAppenderTracked`/`appenderDirs`, `putSplittingRecord`,
  `atomicCommitTestDeps`, `assertFullSplitApplied`, `TestSplitAtomicCommit`'s `newDeps` closure
  and `crashPointTest` closure).
- `engine/split/execute.go` (`ExecuteSplitAtomic`'s WAL-covered commit closure,
  `appendSplitGraphEdges`, `RecoverSplitCommits`).
- `engine/catalog/catalog.go`, `engine/catalog/file.go`, `engine/catalog/recovery.go`,
  `engine/catalog/content.go` (`OpenContentStore`/`ContentPath` — used to derive `root` and
  `catalog.dat`'s path without changing `newTestContentStoreDepsWithWAL`'s signature).
- `engine/btree/insert.go` (`Tree`/`NewTree`/`Root()`), `engine/btree/node.go`
  (`OpenIndexFile`), `engine/btree/lookup.go` (`NewNodeStore`).
- `engine/graph/edge_append.go` (`EdgeAppender`/`OpenEdgeAppender`/`AppendEdgeIfAbsent`).
