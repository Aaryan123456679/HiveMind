# Requirement (fix-cycle 1/3, subtask 4.5.3.2, issue #40)

Respond to CHANGES_REQUESTED verdict in `.cdr/runs/2026-07-11/070-verification/verification.json`
for prior commit `c6840a5` (`engine/split/guard.go` / `guard_test.go`).

Two findings, both scoped strictly to `engine/split/guard.go` and `engine/split/guard_test.go`:

1. **Primary (concurrency_correctness / maintainability)**: `Release`'s doc comment claims the
   `Store(false)`-before-`releaseGuard` ordering is "load-bearing" because reversing it would let a
   racing `TryAcquire` win a double-acquisition. Mutation-testing (reversing the order) did NOT
   reproduce that failure mode -- it instead caused `TestFileGuardRegistryBounded` to fail because
   eviction becomes permanently impossible (gate observes `inProgress` still true). No hook-forced
   test (analogous to `engine/btree/latch.go`'s `unlockOrderHook` /
   `TestNodeLatchUnlockOrderingPreventsDoubleLock`, added in issue #38's `94c24e6`) exists to prove
   or disprove the claimed race deterministically.

2. **Secondary (test_coverage)**: `TestFileGuardRegistryRetainsInProgressEntries` claims to
   "verify the eviction gate directly" but mutation-testing (removing just the `!inProgress`
   clause) does not fail it, because `TryAcquire`'s winning path keeps `refs>0` for the entire held
   window, so the test never isolates `!inProgress` from `refs==0`.

## Acceptance criteria for this fix-cycle

- Investigate (not assume) whether the "load-bearing" ordering claim is actually true and
  reachable. Decide between:
  - (a) if true: add a deterministic `unlockOrderHook`-style hook test proving it, or
  - (b) if false: correct the doc comment to describe the real, verifier-confirmed behavior
    (reversing breaks eviction progress, not correctness) and add/keep a regression test for that
    real behavior, backed by an explicit demonstration that no double-acquisition is reachable.
- Strengthen (or re-scope) `TestFileGuardRegistryRetainsInProgressEntries` so it genuinely
  isolates the `!inProgress` clause from `refs==0`, independently verified by mutation-testing it
  ourselves (temporarily removing the clause and confirming the strengthened test now fails).
- No other files may be touched (scope isolation: other agents concurrently own
  `engine/btree/{delete,insert}.go` + tests, `engine/catalog/content_test.go`,
  `engine/split/orchestrate.go` + test).
- Do not verify our own work; that is a separate step (I4).
