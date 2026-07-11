# Architecture Discovery

Read `gh issue view 39` fresh: subtask 4.5.2.2 traces to regression 2a.2.3
(run `006-verification`, medium, "unresolved doc-overclaim") as recorded in
`.cdr/index/regression.jsonl` at the time issue #39 was filed.

Read the CURRENT `engine/mvcc/gc_test.go` in full for both:
- `TestNewSnapshotClosesEpochAcquireVersionReadRace` (lines ~383-513): the
  deterministic, hook-paused regression test for the narrow epoch-acquire-
  before-CurrentVersion-read TOCTOU, added by commit `acc7601` (2a.2.2 fix).
- `TestGCUnderConcurrency` (lines ~515-741): the broad concurrency stress
  test, its doc comment, and the reader/writer/compactor goroutine loops.

Finding: the doc comment ALREADY correctly disclaims equivalence. It reads
(current text, lines ~522-540):

  "This is NOT a substitute for, nor equivalent to, 2a.2.2's independent-
  verification regression test TestNewSnapshotClosesEpochAcquireVersionReadRace.
  ... This test is instead a broad, general-purpose concurrency stress test:
  ... COMPLEMENTS the deterministic regression test above but does not
  replace it as the guard against that specific, narrow TOCTOU bug class
  reappearing."

The reader-goroutine loop ALSO already uses the shared `stop` channel
(unbounded `for { select { case <-stop: ... } ... }`) instead of a fixed
`readerRounds` count, with per-reader `readerActive` wall-clock-span tracking
and a `minAcceptableOverlapFraction = 0.5` self-consistency assertion that
each reader stayed active for at least half of `testDuration`.

`git log -S "This is NOT a substitute" -- engine/mvcc/gc_test.go` and
`git log -S "readerActive" -- engine/mvcc/gc_test.go` both point to a single
commit: `a1f220d` — "fix(mvcc): correct scope claim and widen reader overlap
in GC stress test (2a.2.3 fix)" (2026-07-05, predates issue #39 being filed
and predates this run). Its commit message documents exactly this subtask's
acceptance criteria: rewriting the doc comment away from "same class of race"
overclaim, backed by an empirical revert-and-rerun experiment (30 runs, zero
detections vs the deterministic test's 1/1 detection), AND removing the fixed
`readerRounds=15` cap in favor of the shared `stop` channel, plus adding the
active-span self-consistency check.

Conclusion: subtask 4.5.2.2 is ALREADY FULLY RESOLVED by pre-existing commit
`a1f220d`, both the required doc-comment fix and the optional stop-channel
widening. This mirrors what a concurrent agent found for sibling subtask
4.5.2.1 (already fixed by `acc7601`, confirmed via docs-only commit
`eca05b6`). No source change is required or warranted; making one would
either be a no-op re-statement of what already exists, or would gratuitously
alter test wording that already satisfies the acceptance criteria (risking
introducing drift/inconsistency with the commit history's own accurate
account of the empirical verification).
