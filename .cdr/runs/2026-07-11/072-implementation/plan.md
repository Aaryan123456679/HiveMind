# Plan

Fix path chosen: **(b)** -- the "load-bearing" mutual-exclusion claim does not hold (confirmed by
direct experiment in architecture-discovery.md); correct the doc, add a real regression test for
the actual, verifier-confirmed behavior instead of an unproven one.

## Steps

1. `engine/split/guard.go`: rewrite `Release`'s doc comment block explaining the
   `Store(false)`-before-`releaseGuard` ordering.
   - Remove the "prevents a racing TryAcquire from winning a double-acquisition" claim.
   - State plainly: this ordering is preferred because it lets eviction actually make progress
     (the gate reads `inProgress`, so clearing it first is a precondition for the gate to ever
     pass for this fileID); reversing it does not create a correctness bug -- it is structurally
     unreachable for reversing this specific ordering to cause a double-acquisition, because the
     eviction gate's blocking clause (`!inProgress`) is the exact same flag being cleared, so
     eviction can never race ahead of the flag itself becoming false.
   - Reference the two new tests by name.
2. `engine/split/guard_test.go`:
   - Add `TestFileGuardReleaseOrderingAffectsEvictionProgressNotCorrectness`: whitebox,
     deterministically replays the reversed order on a real `TryAcquire`-won entry via direct
     `releaseGuard` call, asserts (a) entry retained past due eviction (progress broken), and
     (b) a `TryAcquire` attempted during that exact window still correctly loses (no
     double-acquisition), then finishes clearing state and re-verifies normal recovery.
   - Add `TestFileGuardEvictionGateInProgressClauseIsolated`: whitebox, constructs
     `refs==0 && inProgress==true` directly (bypassing `TryAcquire`'s side effect of holding
     `refs>0`), asserts non-eviction; then flips `inProgress` false at `refs==0` and asserts
     eviction -- isolating the `!inProgress` clause from the `refs==0` clause.
   - Adjust `TestFileGuardRegistryRetainsInProgressEntries`'s doc comment: stop claiming it
     "verifies the eviction gate directly" (it does not isolate the clause); reframe it as an
     end-to-end/integration-level confirmation that a genuinely in-flight fileID survives churn,
     with the isolated clause-level proof now delegated to the new test.
3. Self-consistency: run mutation tests myself --
   - Temporarily remove the `!inProgress` clause from the gate; confirm the NEW isolated test
     fails (and the old integration test does NOT, reproducing the verifier's exact finding, kept
     as documented evidence, not "fixed away").
   - Temporarily reverse Release's Store/releaseGuard order; confirm the NEW ordering test's
     progress-assertion fails as expected (mirrors `TestFileGuardRegistryBounded`), while its
     no-double-acquisition assertion continues to hold (proving the corrected doc's claim).
   - Restore original code after each mutation; run full `go test ./engine/split/... -race` clean.
4. One commit, scoped `git add engine/split/guard.go engine/split/guard_test.go .cdr/runs/2026-07-11/072-implementation`.
5. Write self-consistency.json, handoff.json.
