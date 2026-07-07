# task-2b.5.2 — TestReaderDuringSplit (issue #14, subtask 2 of 2, FINAL subtask of Epic Phase 2b)

## Summary

Second and final subtask under GitHub issue #14 ("[2b] Auto-split concurrent race-test suite", Epic Phase 2b:
Auto-split, the epic's final issue). Adds `TestReaderDuringSplit` to `engine/split/split_race_test.go`: a
concurrent reader races a real `ExecuteSplitAtomic` split and must never observe torn/inconsistent content.

This subtask went through one real implement → verify → fix → re-verify cycle within the CDR protocol; see
Impact for what the verifier caught and how it was fixed.

## Features

- `TestReaderDuringSplit` (`engine/split/split_race_test.go`, lines ~438-574): a reader goroutine repeatedly
  calls `catalog.ContentStore.Read` directly against the pre-split fileID while a concurrent goroutine drives
  a real `ExecuteSplitAtomic` split, asserting the reader never observes truncated/partial/torn content at any
  point in the race.

## Impact

- **Real test-design bug caught by verification, then fixed (not a production bug):** the first version of
  this test (commit `3e95aa2`) pinned an `mvcc.Snapshot` against a separate `mvcc.VersionWriter`-backed root
  from the one `ExecuteSplitAtomic` actually splits (`catalog.ContentStore`). `mvcc.Snapshot.Read()` only ever
  consults `CatalogRecord.CurrentVersion`, which `ExecuteSplitAtomic` never touches (it only mutates
  `Status`/`RedirectTargetIDs`/`SizeBytes`). Because the "reader" and the "split" shared no mutable state, the
  test was a **tautology**: it could never fail regardless of whether the real split logic was correct or
  badly broken, despite launching two real goroutines under `-race`. Independent verification
  (`.cdr/runs/2026-07-07/035-verification/verification.json`) caught this and returned CHANGES_REQUESTED with
  a concrete required change.
- **Fix (commit `d146b33`):** reworked `TestReaderDuringSplit` to have the reader call
  `catalog.ContentStore.Read` directly against the same `ContentStore` instance `ExecuteSplitAtomic` operates
  on — genuinely shared, mutable state. Teeth were confirmed experimentally: a temporary non-atomic rewrite of
  `writeNewContentFile` (truncate + partial write + sleep + partial write, no temp-file/rename) made the
  reworked test fail 5/5 with explicit torn-content messages; reverting restored 5/5 pass, and the
  pre-injection `execute.go` was confirmed byte-identical after revert.
- **Two non-blocking findings carried forward from the final re-verification** (see `.cdr/memory/pending.md`
  for full tracked entries):
  1. A ~1-3% timing-based flake in the test's "sawPostSplit" overlap-confirmation logic (observed via ~265
     cumulative repeated runs across the fix-cycle and re-verification: 1/15, then 1/10 batches, then 1 more
     instance in a full-suite run). The flake always fails safe/loud (a false "reader never observed
     post-split content" failure) and never masks a real bug, but is a CI-reliability cost worth a
     fast-follow.
  2. Issue #14's GitHub wording and the LLD's split-section wording for 2b.5.2 imply true snapshot-isolation
     semantics ("reader sees the pre-split snapshot exactly" / MVCC-mediated isolation), which
     `catalog.ContentStore.Read` cannot actually provide — it is a stateless read-latest call, not wired to
     MVCC. What the test actually proves, and what is actually achievable given the current architecture, is
     the weaker-but-real "content is never torn/partial" invariant. The issue/LLD wording should be corrected
     to match what was actually delivered rather than implying full snapshot isolation.

## Verification

- **First pass**: CHANGES_REQUESTED, run `.cdr/runs/2026-07-07/035-verification/verification.json` (commit
  `3e95aa2`) — tautological test, required rework.
- **Fix cycle**: `.cdr/runs/2026-07-07/036-implementation-fix/` (commit `d146b33`) — reworked test to share
  real `ContentStore` state; teeth confirmed via revert-experiment.
- **Re-verification**: PASS_WITH_COMMENTS, run `.cdr/runs/2026-07-07/037-verification/verification.json`
  (commit `d146b33`) — confirmed genuine shared-state race test with real teeth; two non-blocking follow-ups
  noted above.

## Release Notes

Adds a genuine concurrent-reader-during-split race test proving the auto-split pipeline never exposes torn or
partial content to a concurrent reader. The first version of this test was caught by independent verification
as a tautology (reader and split shared no mutable state) and was reworked to exercise real shared
`ContentStore` state, with its bug-catching teeth experimentally confirmed. No breaking API changes. Two
non-blocking follow-ups (a rare test flake, and issue/LLD wording that overclaims snapshot-isolation semantics)
are tracked in `.cdr/memory/pending.md` rather than blocking this close-out.
