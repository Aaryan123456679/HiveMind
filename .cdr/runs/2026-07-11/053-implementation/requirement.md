# Requirement (fix-cycle, attempt 1/3)

This is NOT a new subtask. It is a fix-cycle responding to a CHANGES_REQUESTED
verdict on subtask 4.5.1.3 (issue #38), commit `545e827b18ff9169c2b9e0ddc7891c79395cc7fa`,
recorded in `.cdr/runs/2026-07-11/047-verification/verification.json` and
`.cdr/runs/2026-07-11/047-verification/handoff.json`.

## Blocking finding (from 047-verification)

`engine/btree/latch.go`'s `Unlock()` calls `l.mu.Unlock()` before `releaseLatch()`.
The doc comment on `Unlock` asserts this ordering is "load-bearing... would break
mutual exclusion entirely" if reversed. The verifier mechanically reversed the
order and ran the full existing test suite (`go test ./engine/btree/... -race
-count=5`, including the stress-based `TestNodeLatchNoDoubleLockAcrossEviction`)
3x with no failures -- i.e. today's test suite provides zero coverage for the
claim, because the existing double-lock test is probabilistic (relies on
uncoordinated goroutine scheduling to hit the narrow eviction-recreate window)
and that window is apparently never actually hit in practice by uncoordinated
scheduling.

The verifier's root-cause read (see `required_action` in 047-verification):
the ordering claim itself is plausible and worth keeping, but is currently
*unproven* by any test -- a future refactor could silently reverse it with
nothing to catch the regression.

## Required fix (this run's mandate)

Per the task brief, prefer option (a): add a deterministic, hook-based test
(mirroring `lookup.go`'s `optimisticReadHook` pattern -- a nil-in-production,
test-only function variable invoked synchronously at one specific point) that
forces the exact race window and empirically distinguishes correct-ordering
(test passes) from reversed-ordering (test fails), giving the "load-bearing"
doc-comment claim real teeth. Fall back to option (b) (soften the comment)
only if investigation shows no realistic deterministic scenario can be
constructed.

## Acceptance criteria for this fix-cycle

1. `engine/btree/latch.go` and `engine/btree/latch_test.go` updated so that a
   new, deterministic test:
   - passes under the current (correct) `mu.Unlock()`-then-`releaseLatch()`
     ordering, and
   - fails under the reversed ordering (confirmed by manually reversing the
     two lines locally, running the new test, observing failure, then
     restoring the correct order -- the reversal itself is NOT committed).
2. No regression in the rest of `engine/btree/...`: `go test ./engine/btree/...
   -race -v -count=5` passes (mirroring 047-verification's own test command,
   scoped to `engine/btree` only per this run's scope isolation -- other
   agents are concurrently touching engine/mvcc, engine/split, engine/wal).
3. Exactly one local commit, touching only `engine/btree/*` and this run's
   `.cdr/` artifacts (explicit `git add` paths, never `-A`/`.`), no push.
4. `handoff.json` explicitly ties this commit back to resolving
   047-verification's CHANGES_REQUESTED finding.
