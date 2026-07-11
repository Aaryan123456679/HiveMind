# Plan

1. In `engine/btree/latch.go`:
   - Add `var unlockOrderHook func(nodeID uint64)`, doc-commented as a
     test-only, nil-in-production hook, mirroring `optimisticReadHook`'s doc
     comment shape from `lookup.go`.
   - In `Unlock`, insert the hook call between `l.mu.Unlock()` and
     `s.releaseLatch(nodeID, l)`.
   - Extend `Unlock`'s existing doc comment with a short pointer to the new
     test (`TestNodeLatchUnlockOrderingPreventsDoubleLock` in
     `latch_test.go`) as the concrete proof of the ordering claim, without
     removing or weakening the existing reasoning (which architecture
     discovery confirmed is factually correct).

2. In `engine/btree/latch_test.go`, add
   `TestNodeLatchUnlockOrderingPreventsDoubleLock`:
   - `store.Lock(nodeID)`, capture `l1 := peekLatch(nodeID)`.
   - Install `unlockOrderHook` (save/defer-restore previous value) that
     signals a `reached` channel then blocks on a `release` channel.
   - Goroutine A: `store.Unlock(nodeID)`.
   - Wait for `reached`.
   - Goroutine B: `store.Lock(nodeID)`; wait for it to complete (with
     timeout).
   - `l2 := peekLatch(nodeID)`.
   - If `l2 == l1`: safe reacquisition of the same object, nothing further to
     check.
   - If `l2 != l1`: this can only be safe if `l1.mu` is already unlocked;
     probe with `l1.mu.TryLock()`. If it succeeds, unlock it again (restore
     state) and continue. If it fails, `t.Fatalf` with an explicit
     double-lock diagnosis message.
   - `close(release)` (via `defer`, so it always executes even on `t.Fatalf`'s
     `runtime.Goexit`, preventing goroutine leaks on failure).
   - Wait for goroutine A to finish, then balance remaining outstanding
     Lock/refs with a final `store.Unlock(nodeID)`.
   - Add reasonable timeouts (`time.After`) on all channel waits so a bug
     that deadlocks instead of racing still fails the test cleanly rather
     than hanging CI.

3. Mutation-test manually (not committed): temporarily swap the two
   statements in `Unlock` (`releaseLatch` before `mu.Unlock()`), run
   `go test ./engine/btree/... -race -run TestNodeLatchUnlockOrderingPreventsDoubleLock -v -count=5`,
   confirm failure with the expected double-lock diagnosis, then restore the
   correct order and re-run to confirm it passes again.

4. Run full self-consistency: `go test ./engine/btree/... -race -v -count=5`.

5. Stage only `engine/btree/latch.go`, `engine/btree/latch_test.go`, and this
   run's `.cdr/runs/2026-07-11/053-implementation/*` files explicitly (never
   `-A`/`.`). One commit, no push.

6. Write `self-consistency.json` and `handoff.json`, tying resolution back to
   `047-verification`.
