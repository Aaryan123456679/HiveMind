# Plan

1. Rewrite `Orchestrator`'s package/struct doc comments to document the corrected design and
   the explicit "future BeginSplit blocked forever after a genuine crash" tradeoff, plus the
   two blocked avenues (guard.go token, Begin/EndSplit signature change) that would be needed
   to close it fully.
2. Change `leases map[uint64]time.Time` -> `leases map[uint64]leaseEntry{deadline, gen,
   reclaimed}`; add `nextGen uint64` counter guarded by `o.mu`.
3. `recordLease`: bump `nextGen`, store a fresh `leaseEntry`.
4. `reclaimIfExpired`: hold `o.mu` for the entire check-then-act sequence (lookup, expiry
   check, `reclaimed` short-circuit, `transitionStatus` call, marking `reclaimed = true`).
   Never call `o.guard.Release`.
5. `EndSplit`: hold `o.mu` across `transitionStatus` + `delete(o.leases, fileID)` (the other
   half of the fencing pair with `reclaimIfExpired`, so the two can never interleave their
   catalog read-then-writes for the same fileID). Keep `guard.Release` as an independent,
   unlocked `defer`, unaffected by outcome (unchanged contract).
6. `BeginSplit`: remove the "retry TryAcquire after a successful reclaim" branch, since
   reclaim no longer frees the guard; always return `ErrAlreadySplitting` on `TryAcquire`
   loss (calling `reclaimIfExpired` first, for its writer-unblocking side effect).
7. Unexport `WithClock`/`WithLeaseDuration` -> `withClock`/`withLeaseDuration` (non-blocking
   note), matching `engine/rpc/interceptor.go`'s `withNow` precedent. Confirmed via grep that
   no caller outside this package's own test file used the exported names.
8. Update `orchestrate_test.go`:
   - Rewrite `TestAbandonedSplittingRecoversAfterTimeout`'s assertions for the corrected,
     guard-preserving behavior.
   - Add `TestReclaimNeverDoubleAcquiresGuardForSlowHolder` (real-goroutine concurrency test).
   - Add `TestEndSplitClearsLeaseForFreshSubsequentAttempt` (white-box lease-map inspection,
     targeting the disclosed EndSplit-clearLease coverage gap).
9. Mutation-test both the pre-existing acceptance test and the two new tests myself:
   - Invert `reclaimIfExpired`'s expiry check (`Before` -> unconditional true) ->
     `TestAbandonedSplittingRecoversAfterTimeout` and
     `TestReclaimNeverDoubleAcquiresGuardForSlowHolder` must fail.
   - Disable `EndSplit`'s `delete(o.leases, fileID)` line -> only
     `TestEndSplitClearsLeaseForFreshSubsequentAttempt` needs to (and does) fail; this is the
     specific gap the verifier's mutation #2 exposed in the pre-fix-cycle code.
10. Run `go build ./engine/split/...`, `go vet ./engine/split/...`, and
    `go test ./engine/split/... -race` (full package, including the untouched
    `split_race_test.go`, to confirm no signature-change compile breakage and no new races).
11. Restore the un-mutated file after each mutation-test run (verified via `diff` against a
    backup) before the final commit.
12. One local commit (Problem/Solution/Impact style), scoped `git add` to exactly the two
    permitted source files plus this run directory. No push, no self-verification.
