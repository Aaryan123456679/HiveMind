# Architecture Discovery — Subtask 4.5.3.3

## Existing `orchestrate.go` state (pre-change)
- `Orchestrator{guard *FileGuard, cat *catalog.Catalog, w *wal.Writer}`.
- `BeginSplit(fileID)`: `guard.TryAcquire(fileID)` (CAS on `fileSplitState.inProgress`,
  `engine/split/guard.go`) then `transitionStatus(fileID, StatusActive, StatusSplitting)`
  (WAL-before-apply via `wal.NewCatalogPutRecord` + `wal.AppendAndApply`, then
  `cat.Put`). On any failure the guard is released. On success the guard is
  left held; caller must eventually call `EndSplit`/`AbortSplit`.
- `EndSplit(fileID, outcome)`: validates `outcome` is `StatusActive` or
  `StatusSplit`, defers `guard.Release(fileID)`, transitions
  `StatusSplitting -> outcome`.
- `transitionStatus` is documented as safe to use as read-then-conditional-write
  (not a dedicated CAS primitive) specifically *because* `BeginSplit`/`EndSplit`
  callers are always externally serialized per fileID by `FileGuard` — no two
  Orchestrator calls for the same fileID can interleave between its `cat.Get`
  and `cat.Put`.
- The package's own doc comment on `Orchestrator` (pre-change) explicitly
  disclosed this exact gap as deliberately out of scope for 2b.1.3, and
  `.cdr/memory/pending.md`'s "Abandoned SPLITTING record has no automatic
  recovery" entry (updated for task-2b.3.6) confirms the intended recovery
  story was never sketched beyond "a lease/heartbeat-based timeout, or an
  explicit crash-recovery repair pass" — no prior partial implementation
  exists to build on or conflict with.

## Concurrency model to fit into
- `FileGuard` (`engine/split/guard.go`, off-limits this run) uses a
  package-level `sync.Mutex` only to guard lazy map creation/eviction; the
  actual per-fileID exclusion is a single `atomic.Bool` CompareAndSwap. This
  mirrors `engine/btree/latch.go`'s `NodeStore` idiom.
- `Orchestrator` itself had no mutex of its own before this change (relies
  entirely on `FileGuard`'s CAS for correctness).
- Repo-wide convention for injectable time sources: `engine/rpc/server.go`
  and `engine/rpc/interceptor.go` both already have a `now func() time.Time`
  struct field defaulting to `time.Now`, overridable via a functional
  `Option` (`WithRecorder` is the existing example of the `Option`
  pattern in that package). No dedicated `Clock` interface/type exists
  anywhere in the repo (checked `engine/mvcc`, `engine/wal`, and a
  repo-wide grep for `Clock`); `engine/mvcc/gc.go`'s `EpochManager` uses a
  purely logical monotonic epoch counter, not a wall-clock abstraction, so
  it is not reusable for a wall-clock lease deadline. Chose to replicate
  the `engine/rpc` "func() time.Time field + functional Option" idiom for
  consistency rather than inventing a third pattern.

## Design constraint from file-scope isolation
Because only `orchestrate.go`/`orchestrate_test.go` may be touched this run,
the lease bookkeeping cannot be persisted on `CatalogRecord` (would require
touching `engine/catalog`) nor stored inside `FileGuard`'s `fileSplitState`
(would require touching `engine/split/guard.go`). It is therefore kept
entirely inside `Orchestrator` itself, as a private
`map[uint64]time.Time` of "abandon after" deadlines, guarded by the
Orchestrator's own new `sync.Mutex` (deliberately separate from
`FileGuard.mu`). This is in-memory, per-`Orchestrator`-instance state; it
recovers a goroutine/in-process "crash" (panic, or simply forgetting to call
`EndSplit`/`AbortSplit`) within the lifetime of one `Orchestrator`, which is
exactly what the test spec describes and exercises. It does NOT recover a
true cross-process restart where a fresh `Orchestrator`/`FileGuard` pair is
constructed with no memory of when the stale on-disk `StatusSplitting`
record's lease began — that would need a persisted lease-start timestamp on
`CatalogRecord`, out of this subtask's file scope and left as documented
future work (see the `Orchestrator` doc comment update in `orchestrate.go`).

## Interaction with reserved/in-flight work in the same package
- `engine/split/guard.go` was confirmed unchanged in its public API
  (`TryAcquire`, `Release`, `InProgress`) by another concurrent agent's
  in-progress edits; `go build`/`go vet` on `engine/split` succeeded
  throughout, confirming no incompatibility.
- `engine/split/split_race_test.go` has uncommitted, in-progress changes
  from a concurrent agent (subtask 4.5.3.6, a `TestReaderDuringSplit`
  flake fix). Running the full `engine/split` package test suite with
  `-race` timed out inside `TestConcurrentAppendSplitRace` (unrelated to
  this subtask's files). Per explicit instruction, this was not treated as
  a blocker: verified separately, by name-listing every test function NOT
  in `split_race_test.go` and running exactly that set (plus this
  subtask's own new test) with `-race`, that everything in scope for this
  change passes cleanly.
