# task-2b.3 — Atomic split-transaction execution (issue #12, CLOSABLE)

## Summary

Issue #12 ("[2b] Atomic split-transaction execution", Epic Phase 2b:
Auto-split) is complete: all 6 subtasks implemented and independently
verified. Together they deliver auto-split's full execution pipeline — from
allocating and writing new files through to a single atomic, WAL-fsynced
commit that makes the split visible and releases writers queued during the
`SPLITTING` window (the primitives issue #10 built but deliberately left
unused):

- **2b.3.1** (`engine/split/execute.go`, commit `01411d8`):
  `ExecuteSplitAllocateAndWrite` — allocates exactly one new fileID per
  `SplitFileProposal` via the shared `catalog.IDAllocator`, and durably
  writes each new content file via a temp-file+rename sequence mirroring
  `ContentStore.writeContentFile`. No catalog/btree/graph mutation, no
  cross-step atomicity yet (deliberately deferred to 2b.3.6).
- **2b.3.2** (commit `73279a0`): `ExecuteSplitRedirectStub` — writes the
  redirect-stub content and transitions the original file's catalog record,
  deliberately *reusing* `originalFileID` as the stub's own identity so that
  any pre-existing inbound reference to it is automatically repointed with
  zero mutation.
- **2b.3.3** (commit `ee2a658`): `ExecuteSplitBtreeInsert` — new-path
  insertion plus old-path repoint, both expressed as safe upserts via
  `btree.Tree.Insert`'s existing upsert semantics.
- **2b.3.4** (commit `3cf7e2c`): new `engine/graph` package — a minimal,
  durable, append-only `EdgeAppender` for `SPLIT_SIBLING`/`REDIRECT` edges,
  built directly on `engine/wal`'s low-level segment writer (fsync + CRC),
  without yet being wired into any crash-recovery replay path.
- **2b.3.5** (commit `8f8f85d`): `ExecuteSplitGraphEdges` — appends a
  complete directed `SPLIT_SIBLING` graph among all new files plus one
  `REDIRECT` edge from `originalFileID` to each new fileID, proving
  pre-existing inbound edges to `originalFileID` survive unchanged.
- **2b.3.6** (commit `be6523d`): `ExecuteSplitAtomic` — the capstone. Wraps
  2b.3.1's writes, 2b.3.2's redirect/catalog transition, 2b.3.3's btree
  update, and 2b.3.5's graph edges inside one new `wal.RecordSplitCommit`
  record, applied via a single `wal.AppendAndApply` closure so the whole
  split commits as one fsynced transaction. Adds `RecoverSplitCommits`
  (idempotent WAL replay) and `AppendEdgeIfAbsent` (idempotent graph-edge
  replay), closing the crash-recovery gap 2b.3.4 deliberately left open and
  2b.3.5 deliberately deferred.

## Narrative: what issue #12 actually delivers

Auto-split's complete execution pipeline is now implemented, independently
verified end-to-end (subtask by subtask, then confirmed as a coherent whole
in 2b.3.6's composition-correctness check), and crash-safe via a single
atomic WAL transaction. Combined with issue #10's detect → guard →
status-transition primitives and issue #11's transport-agnostic
`SplitProposer` abstraction, HiveMind's engine can now: detect a file
crossing its size threshold, guard against duplicate split initiation,
obtain a split plan (real or mocked), execute that plan by allocating and
writing new files, and commit the entire result — catalog, btree, and graph
— atomically, durably, and idempotently on replay, releasing queued writers
only once the commit is visible.

The single highest-risk correctness question across the issue —
whether `ExecuteSplitAtomic` might compose with (rather than supersede) the
earlier per-step functions and reintroduce the non-atomic window they each
individually accepted as a known limitation — was directly checked at
2b.3.6 via code tracing and a repo-wide grep: it does not. The older
per-step functions remain live only as standalone-tested building blocks;
no production code path composes them together with `ExecuteSplitAtomic`.

## Impact / Known Follow-ups

No must-fix findings anywhere across the issue. Non-blocking items tracked
in `.cdr/memory/pending.md` for later follow-up:

1. `newFileIDs` ordering has no canonical cross-function contract
   (2b.3.2/2b.3.1 consistency is currently correct by construction, not by
   an enforced contract).
2. B+Tree keys are raw path strings with no normalization/namespace layer;
   revisit once a canonical topic-path indexing convention is designed.
3. Crash-injection tests in 2b.3.6 replay against the same in-memory
   objects the interrupted call partially mutated, rather than freshly
   reconstructed ones — a materially weaker (though not incorrect) proxy
   for an actual process restart.
4. A cosmetic duplicated/orphaned doc-comment block in
   `engine/wal/record.go` (~lines 342-370), harmless but worth a cleanup
   pass.
5. The btree `SaveRoot`/WAL-replay gap (pre-existing, tracked since Phase 2)
   remains untouched by this issue.
6. The `FileGuard` abandoned-`SPLITTING`-record window (task-2b.1.3) is
   **narrowed, not eliminated**, by 2b.3.6: for the atomic path the
   exposure shrinks from "any time between `BeginSplit` and
   `EndSplit`/`AbortSplit`" to "strictly before `ExecuteSplitAtomic`'s own
   WAL record fsync" — but a crash before that fsync still leaves the
   record stuck `StatusSplitting` with no automatic recovery.

No regressions across all 6 subtasks: `engine/btree`, `engine/catalog`,
`engine/mvcc`, `engine/wal`, and the new `engine/graph` package all remain
green (including `-race` runs) through every subtask's commit.

## Verification

- **2b.3.1**: PASS_WITH_COMMENTS, run `2026-07-07-016-verification`.
- **2b.3.2**: PASS_WITH_COMMENTS, run `2026-07-07-018-verification`.
- **2b.3.3**: PASS_WITH_COMMENTS, run `2026-07-07-020-verification`.
- **2b.3.4**: PASS_WITH_COMMENTS, run `2026-07-07-022-verification`.
- **2b.3.5**: PASS_WITH_COMMENTS, run `2026-07-07-024-verification`.
- **2b.3.6**: PASS_WITH_COMMENTS, run `2026-07-07-027-verification`.
- **Overall**: Issue #12 closable — all 6 subtasks verified, zero must-fix
  findings across the issue.

## GitHub issue #12 state

**Closable but not closed.** All 6 subtasks (2b.3.1-2b.3.6) are implemented
and independently verified locally; the implementation commit for the final
subtask is `be6523d571a6efec25387067ab73267377058d89`. Closure itself
(pushing commits and running `gh issue close`) is explicitly **deferred
pending user authorization to push** — pushes are paused this session — the
same pattern already established for issue #11.

## Release Notes

Issue #12 delivers auto-split's complete execution pipeline: new-file
allocation and durable content writes, a redirect stub reusing the original
fileID (so inbound references need no mutation), B+Tree repointing, an
append-only graph edge log recording `SPLIT_SIBLING`/`REDIRECT` topology,
and — as the capstone — a single atomic, WAL-fsynced transaction committing
all of the above together, with idempotent crash-recovery replay. Combined
with issues #10 (trigger/guard/status) and #11 (`SplitProposer`
abstraction), this completes Epic Phase 2b (Auto-split) end to end. No
breaking API changes; `engine/split` and the new `engine/graph` package are
additive surface only, not yet wired into any live append path outside
their own tests.
