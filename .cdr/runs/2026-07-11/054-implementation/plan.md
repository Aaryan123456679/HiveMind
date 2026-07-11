# Plan — subtask 4.5.2.1

Given the architecture-discovery finding that this subtask's acceptance criteria are already fully
satisfied on `main` (fix commit `acc7601`, re-verified `PASS_WITH_COMMENTS` in
`.cdr/runs/2026-07-05/003-verification/`), the plan is:

1. Do NOT modify `engine/mvcc/read.go`, `engine/mvcc/gc.go`, or `engine/mvcc/gc_test.go` — no
   source change is warranted; the fix and its regression test already exist and pass.
2. Re-run the exact test spec named in the subtask
   (`TestNewSnapshotClosesEpochAcquireVersionReadRace`) plus the full `engine/mvcc` suite under
   `-race` as this run's self-consistency check, to confirm the already-shipped fix still holds at
   current HEAD (concurrent agents on other packages could not have touched `engine/mvcc`, so this
   is a stability confirmation, not new verification of correctness reasoning).
3. Record this determination in `impact-analysis.json` / `validation-matrix.json` /
   `self-consistency.json` / `handoff.json` so downstream tooling can close out issue #39's
   subtask 4.5.2.1 as already-resolved, and so `.cdr/index/regression.jsonl` /
   `.cdr/memory/pending.md` can be updated (by a follow-up doc-maintenance step, not this run) to
   avoid the same stale-tracking issue recurring for other regression-log entries that were fixed
   after their original verification run but never annotated as resolved.
4. Commit only this run's own `.cdr/runs/2026-07-11/050-implementation/` artifacts (no
   `engine/mvcc` files, since none changed) with an explicit path list.
