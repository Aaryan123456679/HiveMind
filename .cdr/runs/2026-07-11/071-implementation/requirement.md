# Requirement — Subtask 4.5.5.3 (Issue #42)

Add a concurrent Append-vs-Read `-race` test for `ContentStore`'s no-torn-read
guarantee.

## Acceptance criteria (from issue)
A new `-race` test interleaves `ContentStore.Append` and `ContentStore.Read`
goroutines on the same `fileID`, empirically pinning down the no-torn-read
guarantee (write-temp-then-rename, content write ordered before `cat.Put`)
against future refactors.

## Test spec
`go test ./engine/catalog/... -race -run TestContentAppendConcurrentRead`

## Impacted modules
`engine/catalog/content_test.go` (test-only; no production code changes)

## Scope constraints (session-level, imposed by orchestrator)
- Touch ONLY `engine/catalog/content_test.go`.
- Do not touch `engine/catalog/content.go` or any other production file —
  other agents concurrently own `engine/btree` (delete.go/insert.go/tests)
  and `engine/split` (guard.go read-only verification, orchestrate.go,
  split_race_test.go).
- `git add` must be explicit (this file + run dir), never `-A`/`.`.
- No `git reset` under any circumstance.
- Test runs scoped to `go test ./engine/catalog/... -race`.
- Ignore stray untracked `engine/engine_stress_test.go` at module root (wrong
  package, being cleaned up in a later pass) — do not touch it.

## Design question to resolve before implementing
Whether the acceptance criteria's "empirically pinning down" wording permits a
purely statistical/iteration-based `-race` test (no forced deterministic
interleaving hook), or whether a hook analogous to btree's
`optimisticReadHook`/`crabRetryHook` is required for determinism.

Resolution (see architecture-discovery.md): statistical/iteration-based is
both sufficient per the acceptance wording AND the only option available
given the hard scope restriction to `content_test.go` only — no hook exists
in `content.go`'s `Append`/`Read`/`writeContentFile` today (unlike
`createWithHook`'s `afterWALBeforeApply` seam, which exists only for
`Create`), and adding one would require editing `content.go`, which is out of
scope for this subtask/session.
