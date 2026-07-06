# Plan — subtask 2b.1.3

## `engine/split/orchestrate.go`

1. Sentinel errors:
   - `ErrAlreadySplitting` — `BeginSplit` refused because `FileGuard.TryAcquire`
     lost (another split already owns this fileID) OR the catalog record's
     `Status` was already non-Active by the time this goroutine (uniquely,
     per the guard) inspected it.
   - `ErrNotSplitting` — `EndSplit` refused because the record's `Status` was
     not `StatusSplitting` when the exit transition was attempted.
   - `ErrSplitInProgress` — `AdmitWrite` refused because `Status ==
     StatusSplitting`.
   - `ErrUnexpectedStatus` — `EndSplit` called with an `outcome` that is not
     one of `StatusActive` (abort) or `StatusSplit` (success) -- defensive
     guard against caller misuse.

2. `type Orchestrator struct { guard *FileGuard; cat *catalog.Catalog; w
   *wal.Writer }` + `NewOrchestrator` constructor validating non-nil guard/
   cat/w (matching this repo's error-return-not-panic convention for invalid
   constructor args).

3. `BeginSplit(fileID)`:
   - `TryAcquire`; false -> `ErrAlreadySplitting`.
   - `cat.Get(fileID)`; error -> release guard, wrap+return.
   - `rec.Status != StatusActive` -> release guard, `ErrAlreadySplitting`
     (covers the crash-restart double-split scenario documented in
     architecture-discovery.md).
   - Build `updated := rec; updated.Status = StatusSplitting`; WAL-log via
     `wal.NewCatalogPutRecord` + `wal.AppendAndApply` (mirroring
     `content.go`'s `createWithHook`/`Append` idiom), apply = `cat.Put`.
   - Any error along the way -> release guard, wrap+return.
   - Success -> return `updated, nil` (guard stays held; this goroutine now
     "owns" the split until it calls `EndSplit`/`AbortSplit`).

4. `EndSplit(fileID, outcome)`:
   - Validate `outcome` is `StatusActive` or `StatusSplit`, else
     `ErrUnexpectedStatus` (guard NOT released for a pure caller-misuse
     argument error -- doesn't correspond to a real split attempt ending).
   - `defer o.guard.Release(fileID)` (guard always released once we get past
     the outcome-validation and actually attempt the transition, per the
     "winner releases once complete, success or failure" contract).
   - `cat.Get(fileID)`; error -> return wrapped (guard still released via
     defer).
   - `rec.Status != StatusSplitting` -> `ErrNotSplitting` (guard still
     released via defer -- unsticks a caller that raced/mis-tracked state).
   - Build `updated := rec; updated.Status = outcome`; WAL-log + apply exactly
     as in `BeginSplit`.
   - Return `updated, nil` or wrapped error.

5. `AbortSplit(fileID)` = `EndSplit(fileID, catalog.StatusActive)` convenience
   wrapper (explicit name for the common "give up, no actual split happened"
   caller path).

6. `AdmitWrite(fileID)`:
   - `cat.Get(fileID)`; error -> wrap+return.
   - `rec.Status == StatusSplitting` -> `ErrSplitInProgress`.
   - Else -> return `rec, nil` (caller proceeds with its own write, e.g. a
     future `ContentStore.Append` call -- not wired here, see scope notes).

## `engine/split/orchestrate_test.go`

`TestSplittingStatusIsolation` (single top-level test, subtests via `t.Run`,
matching `trigger_test.go`'s style), using local `newTestCatalog`/`newTestWAL`
helpers (same shape as `mvcc/write_test.go`'s) plus `engine/mvcc` for the
reader side:

- `sequential_lifecycle`: Put an Active record -> `BeginSplit` succeeds,
  status SPLITTING -> `AdmitWrite` returns `ErrSplitInProgress` ->
  `EndSplit(..., StatusSplit)` succeeds, status SPLIT -> `AdmitWrite` on a
  SPLIT (non-Active, non-Splitting) record: per design, only SPLITTING blocks
  writers explicitly (SPLIT/REDIRECT writer semantics belong to #12); assert
  `AdmitWrite` returns the record without `ErrSplitInProgress` (documents the
  precise boundary).
- `abort_returns_to_active`: `BeginSplit` -> `AbortSplit` -> status back to
  Active -> guard's `InProgress` false -> `BeginSplit` again succeeds (guard
  was actually released, not leaked).
- `second_begin_refused_while_splitting`: `BeginSplit` succeeds once; a
  second `BeginSplit` call for the same fileID (simulating another writer/
  trigger racing in) returns `ErrAlreadySplitting` and does NOT mutate
  status again.
- `end_split_refused_if_not_splitting`: calling `EndSplit` on an Active
  record (no `BeginSplit` first) returns `ErrNotSplitting`.
- `reader_snapshot_unaffected_by_splitting` (the core MVCC-isolation
  assertion): commit version 1 via `mvcc.CommitVersion`; take a `Snapshot`
  BEFORE `BeginSplit`; call `BeginSplit`; assert the pre-existing `Snapshot`'s
  `Read()` still returns version-1 bytes unchanged; also take a FRESH
  `Snapshot` WHILE still SPLITTING and assert it too reads version-1 bytes
  unchanged (no writer advanced `CurrentVersion` during the window, precisely
  because `AdmitWrite` would refuse one); `EndSplit` back to Active; take
  another snapshot post-transition and confirm it also reads version-1 bytes
  (status transitions never touched content).
- `concurrent_writers_and_split_race` (the `-race`-flag-justifying subtest):
  spawn N goroutines calling `AdmitWrite` in a tight loop while one goroutine
  calls `BeginSplit` then (after a short synchronized window) `EndSplit`;
  assert every `AdmitWrite` call observed either a non-SPLITTING record (nil
  error) or `ErrSplitInProgress` -- never any other error, never a torn/
  invalid `Status` value -- and that at least one call of each kind was
  observed (proving the window was actually exercised, not just always-Active
  or always-Splitting by scheduling luck). Runs under `-race` to catch any
  unsynchronized access.

## Self-consistency checks (step 5, not verification)
- `go build ./... && go vet ./... && gofmt -l .` (from `engine/`), expect
  clean/empty gofmt output.
- `go test ./split/... -race -run TestSplittingStatusIsolation -count=5
  -timeout 10m`
- `go test ./split/... -race -v -count=1 -timeout 10m` (full package,
  includes 2b.1.1/2b.1.2's existing tests -- must still pass unmodified).
- Regression: `go test ./catalog/... -count=1 -timeout 5m`,
  `go test ./btree/... -race -count=1 -timeout 10m`,
  `go test ./mvcc/... -race -count=1 -timeout 10m`.
