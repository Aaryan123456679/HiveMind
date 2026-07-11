# Requirement: Subtask 4.5.3.5 (Issue #40, Milestone #10)

Source: `gh issue view 40` (fresh pull, 2026-07-11).

## Subtask 4.5.3.5 — Reconstruct fresh in-memory objects in TestSplitAtomicCommit's crash-injection subtests

**Acceptance criteria**: The 4 crash-point subtests in `TestSplitAtomicCommit` call
`RecoverSplitCommits` against freshly reconstructed `catalog`/`tree`/`appender` objects
(e.g. via `catalog.RecoverFromWAL` re-application onto brand-new, freshly-opened objects)
instead of the same in-memory objects `ExecuteSplitAtomic` partially mutated, making the
simulation faithful to a real process restart.

**Test spec**: `go test ./engine/split/... -race -run TestSplitAtomicCommit`: all 4
crash-point subtests pass against freshly reconstructed objects.

**Impacted modules**: `engine/split/execute_test.go`

## Context / why this was deferred

Deliberately sequenced after 4.5.3.4 (topic-path key normalization in
`engine/split/execute.go`/`execute_test.go`, commit `2a530dd`, follow-up `8d9dc81`) to avoid
a concurrent-edit conflict on `execute_test.go`. 4.5.3.4 is merged; proceeding now.

## Scope guardrails (multi-agent concurrent repo)

- Touch only `engine/split/execute.go` and/or `engine/split/execute_test.go`.
- Do NOT touch `orchestrate.go`/`guard.go` (owned by concurrent 4.5.3.2/4.5.3.3 threads).
- `git add` only exact explicit paths touched by this subtask.
- Test run scoped to `go test ./engine/split/... -race` (package-scoped).
