# Architecture discovery — subtask 4.5.3.6

## Index trail (order followed: index -> memory/handoffs -> LLD -> touched files -> source)

- `.cdr/index/file.jsonl`: no dedicated entry for `engine/split/split_race_test.go`
  (only `docs/LLD/split.md`, `engine/btree/insert*.go`, `engine/catalog/content.go` indexed).
- `.cdr/index/regression.jsonl`: found the prior history of this exact test via a
  compressed-retrieval grep (hash `34c0b6262155a55731200e3a`):
  - `2026-07-07/035-verification` (issue 14, subtask 2b.5.2): CHANGES_REQUESTED — original
    `TestReaderDuringSplit` pinned an `mvcc.Snapshot` against a separate root/content dir the
    real split never touched (tautological test).
  - `2026-07-07/037-verification` (commit `d146b337`): PASS_WITH_COMMENTS, category
    "test-robustness / flaky-test" — the fixed version (reading through the same
    `catalog.ContentStore` the split actually mutates) is genuinely correct but was flagged
    as having a low-severity residual flake risk in its "observed post-split at least once"
    assertion, which is precisely subtask 4.5.3.6.
- `docs/LLD/split.md`: documents the "no torn/partial content during concurrent split" reader
  guarantee this test exercises; no rewrite needed for this subtask (that's 4.5.3.7, out of
  scope here and explicitly deferred).

## Source-level discovery (after index/history exhausted)

- `engine/split/split_race_test.go`: `TestReaderDuringSplit` (subtask 2b.5.2 lineage) ran a
  background reader goroutine for a FIXED `readerIterations = 400` loop, each iteration doing
  `cs.Read(fileID)` then `time.Sleep(10*time.Microsecond)` (~4ms nominal budget, longer in
  practice under scheduler/GC/-race overhead), concurrently with a goroutine driving a real
  `BeginSplit` -> `ExecuteSplitAtomic`. After both goroutines finished, it asserted
  `sawPostSplit > 0` (the reader observed at least one post-split read). This is a sleep-based
  timing ASSUMPTION: if the reader's fixed budget expired before the split's atomic rename
  landed (e.g. under CI load), all 400 reads would return only pre-split content and the test
  would report a false "reader never observed post-split content" failure — the documented
  ~1-3% flake.
- `engine/split/execute.go`: `ExecuteSplitAtomic` already exposes a package-level, nil-in-
  production test seam `atomicCommitHook func(stage string) error` (lines ~535-592), invoked
  via `runAtomicCommitHook(stage)` at well-defined commit stages, already used by
  `TestSplitAtomicCommit`'s crash-injection subtests (execute_test.go — out of scope here).
  The `"before_commit_append"` stage (line ~872) fires immediately AFTER the redirect-stub
  content file has been durably written via `writeNewContentFile`'s write-to-temp-then-
  `os.Rename` technique (line ~858), and BEFORE the WAL commit record / `cat.Put` even happen.
- `engine/catalog/content.go`: `ContentStore.Read` (lines 290-303) resolves `fileID` via
  `cat.Get` (existence check only, not gated on Status) then does a raw `os.ReadFile` of
  `cs.ContentPath(fileID)`. This means the redirect-stub rename in execute.go's
  `writeNewContentFile` is the exact instant a raw `cs.Read` call starts being guaranteed to
  observe post-split bytes — matching the acceptance criterion's own suggested mechanism
  ("a signal channel fired at ExecuteSplitAtomic's atomic rename point").
- Confirmed no `t.Parallel()` calls anywhere in `engine/split/*.go` (grep), so package tests
  run sequentially — safe to mutate the shared package-level `atomicCommitHook` var from this
  test as long as it is restored to `nil` via `defer` before the test returns (done).

## Scope conclusion

The existing `atomicCommitHook` seam in `execute.go` already satisfies the "minimal, tightly
scoped hook, nil-in-production, zero behavior change" bar the launching agent set (mirroring
`unlockOrderHook`) — and it already exists, pre-dating this subtask, added for
`TestSplitAtomicCommit`. No production code file needed to be touched at all; the fix is
100% confined to `engine/split/split_race_test.go`, which is stricter than the scope ceiling
given (which permitted, but did not require, a small hook addition).
