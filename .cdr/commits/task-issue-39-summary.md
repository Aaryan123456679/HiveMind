# Issue #39 — MVCC snapshot/GC correctness follow-ups (Phase 4.5, milestone #10)

## Summary

Issue #39 (milestone #10, "Phase 4.5: Storage-engine technical debt") tracked
two follow-up items against `engine/mvcc/`'s snapshot-epoch and GC-stress test
coverage, both investigated and independently CDR-verified
**PASS_WITH_COMMENTS**:

- **4.5.2.1** — Investigated the issue's TOCTOU concern in `NewSnapshot`
  (epoch-acquire vs. catalog-read ordering). Found the fix already shipped in
  an earlier, independent commit (`acc7601`, predating this issue's filing),
  which reordered `NewSnapshot` to call `em.AcquireCurrentEpoch()` before
  `cat.Get()` and added `TestNewSnapshotClosesEpochAcquireVersionReadRace`.
  No new production code was needed; a docs-only commit (`eca05b6`)
  documented the investigation. The verifier independently re-confirmed via
  direct source read, `git show acc7601`, and mutation testing (reverting the
  ordering reproduced 5/5 deterministic test failures; restored).
- **4.5.2.2** — Investigated whether `TestGCUnderConcurrency`'s doc comment
  overclaimed equivalence to the narrow, deterministic
  `TestNewSnapshotClosesEpochAcquireVersionReadRace` test. Found this was
  already correctly scoped by an earlier, independent commit (`a1f220d`,
  2026-07-05, predating this issue's filing on 2026-07-07), which added
  explicit "complements, does not replace" language backed by an empirical
  revert-and-rerun falsification experiment, and removed a fixed
  `readerRounds` cap in favor of a wall-clock `stop`-channel loop with a
  `minAcceptableOverlapFraction` assertion. No new production or test code
  was needed; a docs-only commit (`6287aee`) documented the investigation.
  The verifier independently re-confirmed via `git show a1f220d`, direct
  source read of the current doc comment and reader-goroutine loop, and
  issue-timeline cross-check.

Together these close out **issue #39**, part of **milestone #10 "Phase 4.5:
Storage-engine technical debt"**. Both subtasks followed the established
"already fixed upstream, re-confirm and document rather than fabricate
unnecessary changes" pattern used elsewhere in this milestone (issue #38's
4.5.1.1, issue #49's 4.5.11.1/4.5.11.3).

## Features / Changes

- No new production behavior changes — both subtasks confirmed pre-existing
  fixes already closed the described gaps.
- `engine/mvcc/gc_test.go`: doc-comment/mechanism content confirmed accurate
  as-is (from `a1f220d`); no edits made in this issue's work.
- `engine/mvcc/read.go`, `engine/mvcc/gc.go`: epoch-acquire-before-catalog-read
  ordering confirmed accurate as-is (from `acc7601`); no edits made in this
  issue's work.
- `.cdr/index/regression.jsonl`: annotated to close the loop on the
  originating findings (subtask 2a.2.2 / 2a.2.3).

## Impact

Confirms two previously-open regression-risk items in the MVCC snapshot/GC
path are genuinely closed, with independent verification rather than trust in
the original fix commits' claims. No behavioral or API change; zero
production risk.

## Verification

| Subtask | Commit    | Verdict              | Run                                          |
|---------|-----------|-----------------------|-----------------------------------------------|
| 4.5.2.1 | `eca05b6` | PASS_WITH_COMMENTS    | `.cdr/runs/2026-07-11/058-verification/`      |
| 4.5.2.2 | `6287aee` | PASS_WITH_COMMENTS    | `.cdr/runs/2026-07-11/062-verification/`      |

`go test ./engine/mvcc/... -race -v -count=2` green across both verification
passes; no regressions.

## Release Notes

- Confirmed (no code change): MVCC snapshot creation is free of the
  epoch-acquire/catalog-read TOCTOU race described in issue #39; the fix
  landed earlier in `acc7601` and is now pinned by regression test
  `TestNewSnapshotClosesEpochAcquireVersionReadRace`.
- Confirmed (no code change): the GC concurrency stress test's documentation
  accurately scopes its guarantee as complementary to, not a replacement for,
  the deterministic TOCTOU regression test.
