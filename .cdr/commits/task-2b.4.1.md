# task-2b.4.1 — Section-index staleness invalidation (issue #13, FINAL/ONLY subtask — closes issue #13)

## Summary

Subtask 2b.4.1 ("Section-index staleness invalidation") was issue #13's sole
subtask (Epic Phase 2b: Auto-split). It closes a "known risk" both
`engine/catalog` and `engine/split`'s LLD docs had flagged since earlier in
Phase 2b: a markdown header-offset cache backing `ReadPartial` could go stale
once a file's boundaries change (append or split), but neither the cache nor
any invalidation mechanism existed yet. This subtask delivered that cache,
`ReadPartial` itself, and an invalidation path wired into every transaction
that changes a file's content boundaries.

The initial implementation (commit `4b8fe670a852ad0bba2bdfa129dae46a054eff75`)
was independently verified and returned **CHANGES_REQUESTED**
(`.cdr/runs/2026-07-07/030-verification/verification.json`): a genuine,
narrow atomicity race on the split path, where a concurrent `ReadPartial`
caller could observe stale cached header offsets in the window between the
split's catalog commit and cache invalidation, because the split path did not
hold the same per-file lock the append path already used. The subsequent fix
cycle (commit `9ba2f0782d43342ae145cdc9db4b22e4c9b98f1c`) closed that race and
added a real `-race` concurrency test that provably catches the bug
(confirmed by reverting the fix and reproducing 10/10 failures, then
restoring it and reconfirming 10/10 passes). Independent re-verification
(`.cdr/runs/2026-07-07/032-verification/verification.json`) returned **PASS**.

This genuine implement -> verify -> fix -> re-verify cycle is exactly the
kind of real correctness issue CDR's verification gate exists to catch, and
it caught one.

## Features

- A per-file, lazily-populated header-offset cache backing a new
  `ReadPartial` read path, with an invalidation hook wired into every
  transaction that mutates a file's content boundaries (append and both
  split-commit paths).
- Split-path invalidation now shares the same per-file locking discipline the
  append path already used, closing the atomicity gap identified in the
  first verification pass.
- A genuine, provably-effective `-race` concurrency test exercising
  `ReadPartial` against a concurrently-running split, in addition to the
  original serial before/after test.

## Impact

`ReadPartial` is now guaranteed to never serve header offsets computed
against stale, pre-mutation content, for both the append and split paths,
under real concurrent access. This retires a risk both `engine/catalog` and
`engine/split`'s LLD docs had explicitly called out as open since earlier in
Phase 2b, and it does so with a verification-caught fix cycle behind it
rather than an unexercised claim.

Since 2b.4.1 was GitHub issue #13's only subtask, this milestone record
closes out issue #13 in full: implementation, a real CHANGES_REQUESTED fix
cycle for an actual stripe-lock atomicity race, and independent
re-verification PASS. **GitHub issue closure itself (closing #13 via the
GitHub API/UI) is deferred pending push authorization** — pushes are paused
this session and were not performed. Issue #13 is verified and committed
locally only, matching the same locally-closed, push-deferred pattern already
used for issues #11 and #12.

## Verification

- **Verdict:** PASS
- **Run ID:** `2026-07-07-032-verification` (re-verification of the fix-cycle
  commit; supersedes the initial `2026-07-07-030-verification`
  CHANGES_REQUESTED verdict on the pre-fix commit)
- Commits: `4b8fe670a852ad0bba2bdfa129dae46a054eff75` (initial implementation),
  `9ba2f0782d43342ae145cdc9db4b22e4c9b98f1c` (fix cycle, re-verified PASS)

## Release Notes

- feat(engine/catalog, engine/split): add a per-file section/header-offset
  cache backing a new `ReadPartial` read path, with invalidation wired
  atomically into both the append and split commit paths, so `ReadPartial`
  never serves stale offsets after a file's content boundaries change.
  Includes a real `-race`-verified concurrency test. Closes GitHub issue #13
  (Section-index staleness invalidation) in full. No breaking API changes.
  GitHub issue closure is deferred pending push authorization; not pushed
  this session.
