# Plan

## Bug 1 fix

1. Add `func (cs *ContentStore) LockFileContent(fileID uint64) (unlock func())`
   to `engine/catalog/content.go`: acquires
   `cs.stripes[stripeFor(fileID)]` (the exact same striped mutex `Append` and
   `ReadPartial` already use) and returns an unlock func. Keeps `cs.stripes`
   itself unexported -- a narrow, purpose-built locking-scope method rather
   than exposing the raw stripe array, per the task's explicit design
   guidance.
2. `engine/split/execute.go`'s `ExecuteSplitRedirectStub`: acquire the lock
   (via `defer unlock()`) before the stub content write, hold it across the
   `cat.Put` + `InvalidateHeaderCache` sequence inside the WAL apply closure,
   release on function return (function does nothing else after invalidate).
3. `engine/split/execute.go`'s `ExecuteSplitAtomic`: acquire the lock before
   the stub content write (outside the WAL closure), hold it through
   `cat.Put` + `InvalidateHeaderCache` inside the closure, then release it
   explicitly right after `InvalidateHeaderCache` -- narrower than the whole
   closure, since the B+Tree inserts and graph-edge appends that follow touch
   entirely different locks `ReadPartial` never takes. An outer
   `lockHeld`-guarded `defer` covers any early-return error path.
4. Lock ordering check (see `LockFileContent`'s doc comment and
   `impact-analysis.json`): `cs.stripes -> wal.Writer-internal -> cat.stripes`
   is the exact nesting order `Append` already establishes; the fix makes
   `engine/split` participate in that same order, not a new one. No inversion
   with `FileGuard` (a separate, independent CAS map keyed by fileID, held by
   the caller before either function is even entered).

## Bug 2 fix

1. Add a new `atomicCommitHook` stage,
   `"after_catalog_put_before_invalidate"`, in `ExecuteSplitAtomic`'s WAL
   apply closure, firing exactly between `cat.Put` and
   `InvalidateHeaderCache` -- the precise window Bug 1 was in. This is a
   test-only synchronization seam (same established pattern as
   `content_test.go`'s `afterWALBeforeApply` hook and this file's existing
   `after_commit_before_catalog_put` / `after_catalog_put_before_btree` /
   `after_btree_before_graph` stages), needed because the real race window is
   normally only nanoseconds wide and not reliably reproducible by pure
   goroutine-scheduling luck.
2. Add `TestSectionIndexInvalidationConcurrent` to
   `engine/split/execute_test.go`: a background goroutine hammers
   `ReadPartial(originalFileID)` in a loop (start barrier via
   `sync.WaitGroup`) while the main goroutine runs `ExecuteSplitAtomic`; the
   hook holds the cat.Put-to-invalidate window open briefly so the reader
   reliably lands in it; assert no `ReadPartial` result observed from the
   moment the hook first fires onward is ever non-empty (stale pre-split
   data).
3. Confirmation step (mandatory per task instructions): temporarily neuter
   the Bug 1 fix in `ExecuteSplitAtomic` (replace `cs.LockFileContent(...)`
   with a no-op), confirm the new test fails deterministically under `-race
   -count=10`, restore the fix, confirm it passes again under the same
   command. See `self-consistency.json` for the recorded results.

## Validation matrix

See `validation-matrix.json`.
