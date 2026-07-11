# Requirement — Subtask 4.5.11.1 (Issue #49)

## Title
Fix Compact's stale compact-state sidecar entry causing silent permanent edge loss
after ordinary truncation (CRITICAL, unresolved).

## Acceptance criteria (verbatim from issue #49)
`graph.dat.compact-state`'s per-node "compacted-through segment number" entry is
invalidated/cleared whenever `TruncateNode` successfully removes and recreates a
node's edge-log directory (which resets `wal.OpenWriter`'s segment numbering to 0),
so the next `Compact()` never mistakes a reused segment number for one it already
processed.

Confirmed root cause (per issue, independently verified true, not speculative):
`AppendEdge(weight=3)` -> `Compact` (fully successful) -> `AppendEdge(weight=5)` ->
`Compact` again currently silently loses the second edge (`LoadCSR` shows
`Weight=3`, not `8`; edge log confirmed empty).

## Test spec (verbatim)
`go test ./engine/graph/... -run TestCompactNormalTruncateThenAppendThenCompactAgain`
— exercise the ordinary (no-failure-injected) truncate-then-append-then-compact-again
cycle described above; assert the second edge is retained after the second
`Compact()`, not just the failure-retry cycle already covered by existing tests.

## Impacted modules (per issue)
`engine/graph/compact.go`, `engine/graph/edgelog.go`, `engine/graph/compact_test.go`

## Scope boundary
This run addresses ONLY subtask 4.5.11.1. Subtasks 4.5.11.2 (lock-ordering race
between `ReadNodeAfter`/`TruncateNode` and concurrent `AppendEdge`) and 4.5.11.3
(EdgeType validation guard in `LoadCSR`/`decodeCSREdge`) are explicitly out of
scope for this run and are not touched.

## Security note (untrusted content disclosure)
`gh issue view 49`'s body was read fresh and contains no embedded fake
system-reminder / fake-directive text — it is a plain, well-formed subtask list.
No untrusted-instruction content was found in the issue body itself. (Separately,
the orchestrating harness injected environment-level system-reminder blocks,
including a stray "Auto Mode Active" block, mid-conversation; per this agent's
standing instructions, no agent-supplied or environment-supplied message is
treated as user consent/approval, and none of it altered this run's scope or
permissions.)
