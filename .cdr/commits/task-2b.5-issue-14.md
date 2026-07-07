# task-2b.5 — Auto-split concurrent race-test suite (issue #14, CLOSABLE — FINAL issue of Epic Phase 2b)

## Summary

Issue #14 ("[2b] Auto-split concurrent race-test suite", Epic Phase 2b: Auto-split) is complete: both
subtasks implemented and independently verified. Issue #14 is the **final issue of Epic Phase 2b** — with it
closed, Epic Phase 2b (issues #10, #11, #12, #13, #14) is now fully implemented, verified, and committed
locally in its entirety.

Together the two subtasks deliver real, non-mocked, `-race`-clean concurrency coverage for the auto-split
pipeline built across issues #10-#13:

- **2b.5.1** (`TestConcurrentAppendSplitRace`, commit `3e95aa2`): many goroutines appending concurrently to
  the same file, driving a real `Orchestrator.BeginSplit` → `ExecuteSplitAtomic` split, asserting no data loss,
  exactly-one-split-per-threshold-crossing, and no dangling graph edges.
- **2b.5.2** (`TestReaderDuringSplit`, commits `3e95aa2` then reworked in `d146b33`): a concurrent reader
  racing a real split, asserting content is never observed torn/partial.

Full details, including the real production bug found in 2b.5.1 and the test-design bug caught and fixed in
2b.5.2's fix cycle, are in `.cdr/commits/task-2b.5.1.md` and `.cdr/commits/task-2b.5.2.md` respectively. This
document is the issue-level milestone record; it deliberately omits file-level implementation detail, which
lives in the per-subtask docs and the underlying `.cdr/runs/` records.

## Features

- `engine/split/split_race_test.go` (new file, 574 lines): both race tests plus shared helpers
  (`driveSplitRound`, `countRedirectRecords`, `collectLeafTags`), exercising the real split machinery built
  across issues #10 (trigger/guard/status), #11 (`SplitProposer` abstraction), #12 (atomic split execution),
  and #13 (section-index staleness invalidation) together under genuine concurrent contention for the first
  time.
- A `SizeBytes` field added to the WAL's `SplitCommitEntry` (`engine/wal/record.go`), required as part of the
  real bugfix described in Impact below.

## Impact

- **Real bug found and fixed in the original implementation (2b.5.1, commit `3e95aa2`):**
  `ExecuteSplitAtomic` and `RecoverSplitCommits` (`engine/split/execute.go`) never created a catalog record
  for newly-split-off fileIDs — split-off files were permanently unreadable/unappendable even though
  btree/graph/content-store state already referenced them as live. Found via direct source-code reading during
  architecture discovery, *before* the race test was ever run — not something the test happened to trip over.
  Fixed by adding matching `cat.Put` calls (status `StatusActive`) on both the live commit path and the
  crash-replay path, threading a new `SizeBytes` field through the WAL commit record so crash-replay has a
  source of truth for the reconstructed catalog record.
- **Real bug caught by the verification process itself, in the first version of `TestReaderDuringSplit`
  (2b.5.2):** the original test (commit `3e95aa2`) pinned an `mvcc.Snapshot` against a separate
  `mvcc.VersionWriter`-backed root from the one `ExecuteSplitAtomic` actually splits
  (`catalog.ContentStore`), and `mvcc.Snapshot.Read()` only consults a catalog field
  (`CurrentVersion`) that `ExecuteSplitAtomic` never touches. Reader and split shared no mutable state, so the
  test was a tautology — it could never fail regardless of whether the real split logic was correct or badly
  broken, despite launching two real goroutines under `-race`. This is exactly the class of finding CDR's
  independent-verification gate exists to catch, and it was caught
  (`.cdr/runs/2026-07-07/035-verification/verification.json`, verdict CHANGES_REQUESTED). It was fixed
  (commit `d146b33`) by reworking the test to have the reader call `catalog.ContentStore.Read` directly
  against the same `ContentStore` instance the split operates on — genuinely shared, mutable state — with the
  fix's bug-catching teeth experimentally confirmed via a revert-experiment (temporarily made
  `writeNewContentFile` non-atomic; reworked test failed 5/5 with explicit torn-content messages; reverting
  restored 5/5 pass). Re-verified PASS_WITH_COMMENTS
  (`.cdr/runs/2026-07-07/037-verification/verification.json`).
- **Two non-blocking findings carried forward from the final re-verification** (both newly logged in
  `.cdr/memory/pending.md` as Phase 2b follow-ups):
  1. `TestReaderDuringSplit` has a ~1-3% timing-based flake in its "sawPostSplit" overlap-confirmation logic,
     observed cumulatively across ~265 repeated runs during the fix cycle and re-verification. It always fails
     safe/loud (a false "reader never observed post-split content" failure) and never masks a real bug, but is
     a CI-reliability cost worth a fast-follow.
  2. Issue #14's GitHub wording and the LLD's split-section wording for 2b.5.2 imply true snapshot-isolation
     semantics ("reader sees the pre-split snapshot exactly"), which `catalog.ContentStore.Read` cannot
     actually provide — it's a stateless read-latest call, not wired to MVCC. The test instead proves the
     weaker-but-real "content is never torn" invariant, which is what's actually achievable given the current
     (disclosed, pre-existing) architectural gap between `engine/mvcc` and `engine/catalog`'s `ContentStore`.
     The issue/LLD wording should be corrected to match what was actually delivered.
- **GitHub issue closure is deferred.** Both commits (`3e95aa2`, `d146b33`) are local-only; pushes are paused
  this session per explicit user instruction. Issue #14 is verified and committed locally, matching the
  pattern already established for issues #11, #12, and #13 in this repo. Actual GitHub issue closure (and any
  push) requires separate, explicit push authorization not granted in this session.

## Verification

- **2b.5.1**: PASS, run `.cdr/runs/2026-07-07/037-verification/verification.json` (never in dispute across the
  issue's fix cycle).
- **2b.5.2**: CHANGES_REQUESTED on first pass (`.cdr/runs/2026-07-07/035-verification/verification.json`,
  commit `3e95aa2`) → fix cycle (`.cdr/runs/2026-07-07/036-implementation-fix/`, commit `d146b33`) →
  PASS_WITH_COMMENTS on re-verification (`.cdr/runs/2026-07-07/037-verification/verification.json`).
- **Overall issue #14 verdict**: **PASS_WITH_COMMENTS**, run_id `037-verification`
  (`.cdr/runs/2026-07-07/037-verification/verification.json`), commit `d146b33790845f913a3cd46ddc80c403d0cc1aaf`.
  Zero must-fix findings remain open; two non-blocking follow-ups tracked in `.cdr/memory/pending.md` (above).

## Release Notes

Issue #14 delivers the auto-split pipeline's concurrent race-test suite: a genuine, `-race`-clean,
no-data-loss/exactly-once-split/no-dangling-edges concurrent-append test, and a genuine concurrent-reader
torn-content test (reworked mid-cycle after independent verification caught the first version as a tautology
that shared no state with the real split path). Along the way, a real production bug was found and fixed:
newly-split-off files were unreadable/unappendable because no catalog record was ever created for them on
either the live commit path or crash-recovery replay. No breaking API changes; additive test coverage plus the
one production bugfix described above.

**This closes GitHub issue #14 — the final issue of Epic Phase 2b (Auto-split).** Combined with issues #10
(split trigger + CAS guard), #11 (`SplitProposer` abstraction), #12 (atomic split-transaction execution), and
#13 (section-index staleness invalidation), Epic Phase 2b is now **fully implemented, independently verified,
and committed locally in its entirety** (issues #10-#14 all done). GitHub-side issue closure for #14 (and for
#13, which is also still pending push per its own commit record) is deferred pending explicit push
authorization — not granted this session.
