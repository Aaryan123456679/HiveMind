# Requirement (fix-cycle, attempt 1 of 3)

Respond to a CHANGES_REQUESTED verdict from `.cdr/runs/2026-07-11/077-verification/verification.json`
on subtask 4.5.3.3 (issue #40), whose actual implementation diff landed in commit ce62682
(bundled with unrelated guard.go work).

## Blocking finding to fix

`FileGuard.Release` (`engine/split/guard.go`, out of scope, DO NOT MODIFY) is unconditional
and has no ownership/fencing concept. `reclaimIfExpired` in `engine/split/orchestrate.go`
was calling `transitionStatus(Splitting->Active)` and then `o.guard.Release(fileID)` purely
on a lease-timeout judgment, without itself having won `TryAcquire`. Concrete failure: a
legitimate holder H that is merely slow (not crashed) past `leaseDuration` could have its
still-live guard hold force-released by a concurrent caller C's `BeginSplit`, letting C start
a second, concurrent split execution over the same fileID. When H eventually called
`EndSplit`, it would clobber C's catalog state and release C's guard in turn -- the exact
double-acquisition FileGuard exists to prevent.

## Non-blocking note (fix if convenient)

`WithClock`/`WithLeaseDuration` are exported; broader public API surface than this repo's
cited clock-injection precedents (`engine/rpc/server.go`'s unexported field,
`engine/rpc/interceptor.go`'s unexported `withNow` Option). Consider unexporting.

## Scope constraints (hard)

- ONLY touch `engine/split/orchestrate.go` and `engine/split/orchestrate_test.go`.
- Must NOT touch `engine/split/guard.go` -- if a guard.go change is determined to be
  unavoidable for full fencing, STOP and report instead of touching it.
- Must NOT change `BeginSplit`/`EndSplit`/`AbortSplit` exported signatures in a way that
  would break `engine/split/split_race_test.go` (untouchable, calls these with today's
  fileID-only signatures) -- this is a hard compile-time constraint, not just a scope
  guideline.
- Test runs scoped to `go test ./engine/split/... -race`.

## Required deliverables

1. A fencing/generation scheme, entirely within `orchestrate.go`'s own state, so that a
   reclaim can only act on the exact lease/generation it decided to reclaim -- if the
   legitimate holder already completed by the time of the actual reclaim action, the reclaim
   must be a no-op instead of blindly force-reverting.
2. A concurrency test proving the "legitimate holder merely slow, not crashed" scenario does
   NOT result in double-acquisition/concurrent-double-execution.
3. A test specifically covering `EndSplit`'s lease-clearing effect (the disclosed
   test-coverage gap: disabling `clearLease` previously still passed the acceptance test).
4. One local commit (Problem/Solution/Impact style), no push.
5. No self-verification (invariant I4) -- that is `/cdr:verify`'s job.
