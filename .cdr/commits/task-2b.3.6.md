# task-2b.3.6 — Atomic split commit (issue #12, FINAL subtask)

## Summary

Sixth and final subtask under GitHub issue #12 ("[2b] Atomic split-transaction
execution", Epic Phase 2b: Auto-split). Adds `ExecuteSplitAtomic` to
`engine/split/execute.go`: a new `wal.RecordSplitCommit` WAL record type that
wraps the catalog `Put` (status transition), btree insert/repoint, and graph
edge appends inside a single `wal.AppendAndApply` closure, so the entire
split — allocation, content writes, catalog update, btree update, and graph
edge writes — commits atomically as one fsynced WAL transaction before the
split becomes visible and queued writers are released. `RecoverSplitCommits`
provides an idempotent replay pass over `RecordSplitCommit` entries (upsert
semantics for catalog/btree, `AppendEdgeIfAbsent` for the graph log, which
reads back the edge log and appends only if a byte-identical edge is not
already present).

`ExecuteSplitAtomic` re-implements 2b.3.2/2b.3.3/2b.3.5's logic inline rather
than composing their separately-exported functions, closing the highest-risk
correctness surface flagged across the issue: the old staged two-step flow
(reaching `StatusSplit` via a separate `EndSplit` call) is genuinely
superseded, not additionally run alongside the new atomic path.

## Features

- `ExecuteSplitAtomic(...)`: single `wal.AppendAndApply` closure durably
  fsyncs `wal.RecordSplitCommit` before catalog `Put`, btree insert, and
  graph edge appends are applied; content-file writes happen before the
  fsync but are inert/unreferenced until the record is durable, so a crash
  in that window leaves only harmless orphan files.
- `RecordSplitCommit` (new WAL record type, `engine/wal/record.go`): follows
  the existing "skip record types I don't own" convention used by
  `catalog.RecoverFromWAL`.
- `RecoverSplitCommits`: idempotent crash-recovery replay pass — re-applies
  `cat.Put` (upsert), `tree.Insert` (upsert), and
  `graph.EdgeAppender.AppendEdgeIfAbsent` (dedupes via full-log scan) for
  every `RecordSplitCommit` found in the WAL.
- `AppendEdgeIfAbsent` (`engine/graph/edge_append.go`): idempotent variant of
  `AppendEdge` — reads the full edge log and appends only if no
  byte-identical `(Source,Target,Type)` edge already exists.
- Queued-writer release: `guard.Release(originalFileID)` is called only
  after every step inside the apply closure succeeds, confirmed to be the
  first point `Status` leaves `StatusSplitting` in the real code path.

## Impact

Subtask 6 of 6 under issue #12 — **FINAL**. All 6 subtasks (2b.3.1 through
2b.3.6) are now implemented and independently verified; issue #12 is
closable pending only the push-and-close step.

Carried forward from verification:

1. **Verified NOT to reintroduce the non-atomic window.** `ExecuteSplitAtomic`
   re-implements 2b.3.2's redirect-stub logic, 2b.3.3's btree
   insert/repoint, and 2b.3.5's graph edge writes inline inside its own
   single `wal.AppendAndApply` closure rather than calling
   `ExecuteSplitRedirectStub`, `ExecuteSplitBtreeInsert`, or
   `ExecuteSplitGraphEdges` directly. A repo-wide grep across all
   non-test `.go` files confirms zero production call sites compose
   `ExecuteSplitAtomic` with those older, separately-exported functions —
   they remain live only for their own subtask's standalone tests and are
   dead code from any real split-flow perspective. This was the single
   highest-risk item across the whole issue and it held up under direct
   code tracing.
2. **Non-blocking test-coverage gap.** The 4 crash-injection subtests in
   `TestSplitAtomicCommit` call `RecoverSplitCommits` against the *same*
   in-memory `cat`/`tree`/`appender` objects partially mutated by the
   interrupted `ExecuteSplitAtomic` call, rather than reconstructing fresh
   objects (e.g. via `catalog.RecoverFromWAL`) to more faithfully simulate
   an actual process restart. Achievable for the catalog today; worth a
   follow-up test, not blocking given btree's own pre-existing,
   disclosed root-reconstruction gap already makes a fully faithful
   full-stack restart simulation impossible.
3. **Cosmetic maintainability comment.** `engine/wal/record.go` has a
   duplicated/orphaned `AppendAndApply` doc-comment block (~lines 342-370)
   left over from the diff, sitting awkwardly before the `--- SplitCommit
   ---` section header. Harmless — does not affect compilation, `gofmt`,
   `vet`, or behavior. Tracked in `.cdr/memory/pending.md` for later
   cleanup.
4. **Residual risks accurately disclosed, not resolved.** The btree
   `SaveRoot`/WAL-replay gap remains untouched (pre-existing, tracked
   separately under Phase 2). The `FileGuard` abandoned-`SPLITTING`-record
   window (task-2b.1.3) is **narrowed but not eliminated**: pre-2b.3.6 the
   exposure was "any time between `BeginSplit` and `EndSplit`/`AbortSplit`";
   post-2b.3.6, for the atomic path, it is "strictly before this
   function's own WAL record fsync" — materially smaller, but the same
   class of gap still exists if a crash happens before that fsync.

No regressions: `go build ./...`, `go vet ./...`, `gofmt -l .` all clean;
`go test ./...` (engine, btree, catalog, graph, mvcc, split, wal) green,
including `-race` runs and `TestSplitAtomicCommit -count=5`.

## Verification

- Verdict: `PASS_WITH_COMMENTS`
- Run ID: `2026-07-07-027-verification`
- Dimensions: requirements_conformance PASS, architecture_conformance PASS,
  composition_correctness (no non-atomic window reintroduced) PASS,
  atomicity_claim PASS, idempotent_replay PASS,
  crash_injection_test_scrutiny PASS_WITH_COMMENTS (in-memory-object replay
  gap, non-blocking), release_queued_writers PASS, residual_risk_assessment
  PASS (accurately characterized), backward_compatibility PASS,
  maintainability COMMENT (orphaned doc-comment block), regression_risk
  PASS, security PASS, confidence HIGH.
- No must-fix findings; two low-severity findings tracked in
  `.cdr/memory/pending.md`.

## Release Notes

- feat(engine/split): commit the entire split — allocation/content writes,
  catalog status transition, btree insert/repoint, and graph
  SPLIT_SIBLING/REDIRECT edges — as a single atomic, WAL-fsynced
  transaction (`ExecuteSplitAtomic` + `wal.RecordSplitCommit`), with
  idempotent crash-recovery replay (`RecoverSplitCommits`,
  `AppendEdgeIfAbsent`); queued writers are released only after the commit
  is durable. Closes out GitHub issue #12 (Auto-split, all 6 subtasks now
  implemented and verified). No breaking API changes.
