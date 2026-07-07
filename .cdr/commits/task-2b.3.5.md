# task-2b.3.5 — Split graph edge wiring (SPLIT_SIBLING + redirect)

## Summary

Fifth of 6 subtasks under GitHub issue #12 ("[2b] Atomic split-transaction
execution", Epic Phase 2b: Auto-split). Wires `engine/split/execute.go` to
`engine/graph`'s `EdgeAppender` (task-2b.3.4) for the first time:
`ExecuteSplitGraphEdges` appends a `SPLIT_SIBLING` edge between every pair of
newly split-off files (a complete directed graph among the new fileIDs), and
one `EdgeRedirect` edge from `originalFileID` to each new fileID.

Key insight carried forward from 2b.3.2: any pre-existing inbound edge that
targeted `originalFileID` before the split already points at the redirect
stub, because 2b.3.2 deliberately reuses `originalFileID` as the stub's own
identity rather than allocating a new one. This subtask therefore requires
**zero mutation** of existing inbound edges to satisfy the "repoint inbound
edges to redirect stub" acceptance criterion — the repointing is a structural
consequence of 2b.3.2's design, not new logic added here.

## Features

- `ExecuteSplitGraphEdges(appender, originalFileID uint64, newFileIDs []uint64) error`:
  appends `SPLIT_SIBLING` edges for every ordered pair among `newFileIDs`
  (complete directed graph, N*(N-1) edges) and one `EdgeRedirect` edge from
  `originalFileID` to each new fileID.
- Degenerate-input handling: nil appender, nil/empty `newFileIDs`, and
  single-new-file (N=1, no sibling edges possible) all handled without panic.
- Test proves, via `EdgeAppender.ReadAll()`, that a pre-existing inbound edge
  targeting `originalFileID` (written before the split) is present unchanged
  in the edge log after the split executes — the strongest claim provable
  today given `engine/graph` has no query/resolve API yet.

## Impact

Subtask 5 of 6 under issue #12 — penultimate; only 2b.3.6 remains.

Non-blocking follow-ups carried forward from verification:

1. The inbound-edge-preservation test demonstrates the edge is unchanged in
   the append-only log, which is the strongest provable claim available
   without a graph query/resolve API. Worth revisiting once Epic 3's
   traversal layer lands, to assert resolution semantics directly rather
   than log-presence.
2. The complete-directed-graph sibling topology is O(N^2) edges among N new
   files, doubling storage relative to a simpler star/chain topology, on a
   log that currently has no reader at all. Defensible now (log is
   unconsumed; correctness > compactness pre-Epic-3) but should be revisited
   once a real consumer/reader of `engine/graph` exists.

Crash-recovery gap disposition: the `engine/graph` edge-append
crash-recovery gap tracked in `.cdr/memory/pending.md` (surfaced at
task-2b.3.4) is **correctly and deliberately not resolved by this subtask**.
2b.3.5's scope is edge *content* (which edges to write), not edge *durability
under crash* (how writes get folded into recovery). That responsibility now
rests entirely with 2b.3.6, whose own acceptance criteria explicitly list
"graph edge writes" as part of what its single WAL-covered, fsynced
transaction must atomically commit alongside allocation, content writes,
catalog updates, and btree updates.

## Verification

- Verdict: `PASS_WITH_COMMENTS`
- Run ID: `2026-07-07-024-verification`
- Dimensions: requirements_conformance PASS, architecture_conformance PASS,
  test_coverage PASS_WITH_COMMENT, regression_risk PASS, security PASS,
  maintainability PASS, crash_recovery_deferral PASS,
  sibling_topology_design_choice COMMENT_NON_BLOCKING.
- No regressions; no missing-context findings.

## Release Notes

- feat(engine/split): wire ExecuteSplitGraphEdges to engine/graph's
  EdgeAppender — SPLIT_SIBLING edges among new files, REDIRECT edges from
  originalFileID to each new fileID; pre-existing inbound edges to
  originalFileID require no mutation since 2b.3.2 reuses that fileID as the
  redirect stub's identity.
