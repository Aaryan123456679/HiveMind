# Requirement — Subtask 4.5.3.3 (Issue #40)

Add automatic recovery/timeout for abandoned `StatusSplitting` catalog records.

## Acceptance criteria
A catalog record left in `catalog.StatusSplitting` because its split holder
crashed between `Orchestrator.BeginSplit` and `EndSplit`/`AbortSplit` must be
automatically reverted (e.g. via a lease/heartbeat-based timeout) rather than
permanently blocking future `BeginSplit` calls (`ErrAlreadySplitting`) for
that fileID forever.

Builds on task-2b.3.6's WAL-covered `ExecuteSplitAtomic` commit path (which
narrowed, but did not eliminate, this gap — see
`.cdr/memory/pending.md`'s "Abandoned SPLITTING record has no automatic
recovery" entry).

## Test spec
`go test ./engine/split/... -race -run TestAbandonedSplittingRecoversAfterTimeout`:
simulate a crash mid-split (no `EndSplit`/`AbortSplit`), advance past the
lease timeout, assert the record reverts and a subsequent `BeginSplit` for
the same fileID succeeds.

## Impacted modules (per issue #40)
- `engine/split/orchestrate.go`
- `engine/split/orchestrate_test.go`

## Scope isolation (this run)
Per the launching agent's explicit instructions, only the two files above
may be touched. `engine/split/guard.go` (read-only under concurrent
verification) and `engine/split/split_race_test.go` (concurrently being
edited by another in-flight agent, subtask 4.5.3.6) must not be modified,
even though both are in the same package.
