# Issue #49 — Storage-engine correctness fixes (Phase 4.5, milestone #10)

## Summary

Issue #49 (milestone #10, "Phase 4.5: Storage-engine technical debt")
tracked three storage-engine correctness gaps in `engine/graph/` and
`engine/wal/`, all now implemented and independently CDR-verified
**PASS_WITH_COMMENTS**:

- **4.5.11.1** — Investigated a CRITICAL-severity risk described by the
  issue: sidecar-staleness/data-loss from WAL segment-number reuse after a
  node is truncated then recreated during `Compact`. Found the underlying
  fix already shipped in an earlier, independent commit (`ed57468`,
  `wal.WriteSegmentFloor`) that predates this issue's work. The verifier
  independently re-traced the fix logic against the issue's exact failure
  mode and added the issue's required exact-named regression test,
  `TestCompactNormalTruncateThenAppendThenCompactAgain` (commit `2e415e5`),
  to pin the already-correct behavior going forward.
- **4.5.11.2** — Fixed a genuine, previously-unguarded lock-ordering race
  between `Compact`'s `ReadNodeAfter` → `TruncateNode` sequence and a
  concurrent `AppendEdge` for the same node, which could silently drop a
  concurrently-appended edge. Closed via a new per-node `*sync.Mutex`
  mechanism (`nodeLocksMu`/`nodeLocks`/`LockNode` in
  `engine/graph/edgelog.go`, `heldNodeLocks`/`releaseHeldLocks` in
  `engine/graph/compact.go`) making "AppendEdge for node X" and "Compact's
  read-then-truncate of node X" strictly mutually exclusive, scoped per
  node (not global). Proven via a new `compactNodeLockedHook` test seam and
  `TestCompactConcurrentAppendNotLost` (commit `0ed8461`).
- **4.5.11.3** — Investigated the issue's requirement that the CSR
  (`engine/graph/csr.go`) read path validate on-disk `EdgeType` bytes
  before use, matching `edge_append.go`'s `decodeEdge` convention now that
  `PutEdge` gives graph.dat a second real write path. Found the guard
  already present (`ValidEdgeType`, from earlier subtask 3.1.4, already
  invoked in `decodeCSREdge`). The only gap was the issue's exact-named
  regression test, added as `TestLoadCSRRejectsUnknownEdgeType` in
  `engine/graph/csr_test.go` (commit `69fa26e`), independently
  mutation-tested by the verifier to confirm it is non-vacuous.

Together these close out **issue #49**, part of **milestone #10 "Phase 4.5:
Storage-engine technical debt"**. One of the three subtasks (4.5.11.2)
delivered a genuine new correctness fix; the other two (4.5.11.1, 4.5.11.3)
confirmed pre-existing correct behavior and added the issue's required
regression coverage, each independently re-verified rather than accepted on
the strength of the investigation alone.

## Features

- **WAL segment-reuse regression coverage** — `TestCompactNormalTruncateThenAppendThenCompactAgain`
  pins `wal.WriteSegmentFloor`'s existing fix for the truncate-then-recreate
  segment-number-reuse hazard against `Compact`'s normal operation path.
- **Per-node lock-ordering fix** — `engine/graph/edgelog.go`'s
  `LockNode`/`nodeLocks` and `engine/graph/compact.go`'s
  `heldNodeLocks`/`releaseHeldLocks` make `AppendEdge` and `Compact`'s
  read-then-truncate sequence for the same node strictly mutually
  exclusive, closing a real concurrent-edge-loss window. A new
  `compactNodeLockedHook` test seam (matching the repo's existing
  `crabRetryHook`/`atomicCommitHook` idiom) and
  `TestCompactConcurrentAppendNotLost` prove it under `-race`.
- **CSR EdgeType validation regression coverage** — `TestLoadCSRRejectsUnknownEdgeType`
  pins `decodeCSREdge`'s existing `ValidEdgeType` guard, isolating the
  type-validation assertion from the CRC-mismatch path it's otherwise
  coupled to.

## Impact

- The genuine correctness fix (4.5.11.2) removes a real, previously-latent
  window where a concurrent `AppendEdge` landing during `Compact`'s
  read-then-truncate sequence for the same node could be silently lost.
  The fix is scoped per node, not global — `AppendEdge` for any other node
  is unaffected, and only nodes actively being compacted are blocked, only
  for that `Compact` run's duration.
- 4.5.11.1 and 4.5.11.3 confirm two previously-unverified pieces of
  storage-engine correctness (WAL segment-floor handling; CSR EdgeType
  validation) are already sound, and lock both down with issue-mandated,
  exact-named regression tests so future changes can't silently regress
  them.
- Full `engine/graph` and `engine/wal` suites pass under `-race` throughout
  (including standalone repeated runs of the new concurrency test,
  `-count=20`, with zero flakes observed by the verifier).
- **Non-blocking, disclosed gaps carried forward** (none blocking, all
  independently confirmed during verification — see per-subtask
  verification runs for full detail):
  - 4.5.11.2: multi-node simultaneous lock-holding behavior (e.g. lock
    acquisition ordering across more than one node at once) is not
    documented or explicitly tested; the deadlock-safety of the
    single-node-at-a-time case was independently stress-tested and
    confirmed sound.
  - 4.5.11.3: `TestLoadCSRRejectsUnknownEdgeType`'s hand-patched
    invalid-type fixture only patches the type byte of the first
    (edge-index-0) encoded edge; multi-edge-index coverage of the same
    guard remains a minor, non-blocking coverage gap.
- All three subtasks' commits (`2e415e5`, `0ed8461`, `69fa26e`) already
  follow the Problem/Solution/Impact commit-message standard — no
  deviation to note, no git history rewrite needed.
- Consistent with prior issue-close precedent: all three commits are
  local-only (not pushed), and issue #49 is not being closed on GitHub as
  part of this step — a separate step handles push/close.

## Verification

| Subtask | Commit | Verdict | Verification run |
|---|---|---|---|
| 4.5.11.1 | `2e415e5` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/036-verification` |
| 4.5.11.2 | `0ed8461` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/038-verification` |
| 4.5.11.3 | `69fa26e` | PASS_WITH_COMMENTS | `.cdr/runs/2026-07-11/040-verification` |

## Release Notes

- Fixed a lock-ordering race in `engine/graph` where a concurrent
  `AppendEdge` could be silently lost if it landed during `Compact`'s
  read-then-truncate sequence for the same node. `AppendEdge` and
  `Compact` now hold a per-node lock across that sequence, closing the
  window without affecting concurrent work on other nodes.
- Added regression tests pinning two previously-unverified pieces of
  storage-engine correctness: WAL segment-number-reuse handling after
  node truncate+recreate (`TestCompactNormalTruncateThenAppendThenCompactAgain`),
  and CSR on-disk `EdgeType` validation
  (`TestLoadCSRRejectsUnknownEdgeType`). Both confirmed the underlying
  behavior was already correct; no production-code change was required for
  either.
- This closes issue #49 under milestone #10 "Phase 4.5: Storage-engine
  technical debt."
- Known, disclosed, non-blocking follow-up: document/test multi-node
  simultaneous lock-holding behavior for the new per-node lock mechanism
  (currently only the single-node-at-a-time case is tested and
  documented).
