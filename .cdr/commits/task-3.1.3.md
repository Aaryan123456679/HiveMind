# task-3.1.3 — Periodic edge-log compaction into CSR graph.dat (issue #15, Epic Phase 3)

## Summary

Subtask 3.1.3 ("periodic compaction of the per-node edge log into CSR graph.dat") is complete and
independently verified. This subtask feeds 3.1.2's durable, append-only per-node edge log
(`engine/graph/edgelog.go`) back into 3.1.1's read-optimized CSR adjacency snapshot
(`engine/graph/csr.go`), so `graph.dat` actually reflects newly discovered edges over time, merging
`ENTITY_COOCCUR` weights (summed) and applying last-write-wins semantics for other edge types.

This subtask had an unusually long fix cycle — two real, independently-confirmed correctness bugs were
found and fixed across three verification passes before reaching a clean state. Both bugs were in the
crash-safety/retry machinery around compaction, not in the core merge logic itself, and both were caught
by the verification process (the second one via the verifier's own exploratory testing, beyond the given
test spec) before ever shipping. This record narrates that full arc, since it is a genuine example of the
verification gate doing its job on a subtle, compounding correctness class of bug.

## Features

- `engine/graph/compact.go`: `Compact()` reads each node's pending edge-log entries, merges them into the
  existing CSR snapshot (summing `ENTITY_COOCCUR` weights, last-write-wins for other edge types), and
  atomically rewrites `graph.dat` via the existing `WriteCSR` temp+fsync+rename path, then truncates the
  now-merged portion of the per-node edge log.
- `graph.dat.compact-state` sidecar (per node, durable, atomic temp+fsync+rename): records the
  compacted-through segment number so a retry after a failed truncation does not re-sum already-durably-merged
  edge-log entries. New `EdgeLog.ReadNodeAfter` consults this to skip already-reflected segments on retry.
- `wal.WriteSegmentFloor`: a durable, monotonic per-node floor (atomic temp+fsync+rename), recording one past
  the highest WAL segment number ever used for that node's edge log. Written before segment removal in
  `TruncateNode`, guaranteeing segment numbers are never reused after a node directory is truncated — closing
  the gap where truncation-triggered renumbering could collide with a stale sidecar value.

## Impact — bug-discovery arc

- **Original implementation** (commit `ebbc1ff`, run `.cdr/runs/2026-07-08/007-implementation/`): built the
  compaction pass and initial crash-safety design — atomic `graph.dat` write via `WriteCSR`, then
  `TruncateNode` on the edge log.

- **F1 — retry double-counting** (found in verification, run `.cdr/runs/2026-07-08/008-verification/`,
  verdict CHANGES_REQUESTED): a retry after a failed truncation re-summed edge-log entries that had already
  been durably merged into `graph.dat`, permanently corrupting/compounding `ENTITY_COOCCUR` weight on every
  retry (e.g. 3 -> 6 -> 9 -> ...). Empirically proven via reproduction, not just theorized.
  - **F1 fix** (commit `9850083`, run `.cdr/runs/2026-07-08/009-implementation/`): added the durable
    per-node `graph.dat.compact-state` sidecar described above, written atomically after `WriteCSR`'s rename
    and before `TruncateNode`, plus the new `EdgeLog.ReadNodeAfter` skip-already-reflected-segments logic.

- **F2 — silent permanent data loss on the ordinary happy path** (found in verification, run
  `.cdr/runs/2026-07-08/010-verification/`, verdict CHANGES_REQUESTED): this pass independently reconfirmed
  F1's fix was correct via its own revert-experiment, but the verifier's own exploratory testing — beyond the
  given test spec — surfaced a new, more severe bug: F1's fix caused silent, **permanent** data loss on the
  completely ordinary happy path, with **no crash required**. `TruncateNode` fully deleted a node's edge-log
  directory, so WAL segment numbering restarted at 0 for that node, colliding with the stale sidecar's
  "compacted-through segment N" entry. The very next real edge appended to that node was then silently
  skipped by the next compaction pass, forever.
  - **F2 fix** (commit `ed57468`, run `.cdr/runs/2026-07-08/011-implementation/`): the implementer evaluated
    two alternatives — clearing the sidecar on successful truncation vs. never reusing segment numbers — and
    chose the latter as strictly safer under both bug scenarios. `wal.WriteSegmentFloor` durably records a
    monotonic floor (atomic temp+fsync+rename), one past the highest segment number ever used for a node,
    written **before** segment removal in `TruncateNode`. A crash between the floor write and removal just
    leaves not-yet-removed segments in place — safe, and matches F1's already-tested retry behavior — instead
    of causing data loss.

- **Final verification** (run `.cdr/runs/2026-07-08/012-verification/`, verdict **PASS_WITH_COMMENTS**, after
  resuming from a session-limit interruption mid-run): independently redid all revert-experiments via a git
  worktree rather than trusting the implementer's transcript; ran additional self-constructed fault-injection
  tests beyond the implementer's own suite (crash between floor-write and segment removal, floor-write's own
  atomicity, sidecar/floor consistency across 5 append/compact rounds); confirmed both the
  first-ever-compaction path and the floor-persists-across-a-node's-full-lifetime path are correct. Full test
  matrix green: `TestCompaction` and `TestTruncateNode` x10 under `-race`, `wal` package x5 under `-race`, and
  the full module suite clean except the pre-known, unrelated `TestReaderDuringSplit` flake (already tracked,
  see `.cdr/memory/pending.md`).

## Known non-blocking finding (pre-existing, not introduced by this fix cycle)

`EdgeLog.ReadNodeAfter` does not hold `l.mu`, and `TruncateNode` re-lists and deletes whatever segments
currently exist rather than the exact set `Compact` read — a theoretical race with a concurrent `AppendEdge`.
This was traced and confirmed present with identical mechanism and severity in all three commits at this seam
(`ebbc1ff`, `9850083`, `ed57468`) — it predates this fix cycle and was not introduced or worsened by either
F1's or F2's fix. Already logged in `.cdr/memory/pending.md` from the `010-verification` pass, with a
forward-reference to GitHub milestone #10 ("Phase 4.5: Storage-engine technical debt & correctness
follow-ups", issues #38-42); confirmed present in this final pass too, not a new finding, not duplicated here.

## Verification

- **008-verification**: CHANGES_REQUESTED (F1: retry double-counting) —
  `.cdr/runs/2026-07-08/008-verification/verification.json`, commit reviewed `ebbc1ff`.
- **010-verification**: CHANGES_REQUESTED (F2: silent permanent data loss on the happy path, found via the
  verifier's own exploratory testing) — `.cdr/runs/2026-07-08/010-verification/verification.json`, commit
  reviewed `9850083`.
- **012-verification (final)**: **PASS_WITH_COMMENTS** —
  `.cdr/runs/2026-07-08/012-verification/verification.json`, commit reviewed `ed57468`. Zero blocking
  findings. One non-blocking, pre-existing lock-ordering gap carried forward (see above). Full adversarial
  revert-experiments for both F1 and F2 independently redone via git worktree, not merely trusted from commit
  messages.

## Release Notes

Subtask 3.1.3 (issue #15, Epic Phase 3) delivers periodic compaction of the per-node edge log into the CSR
`graph.dat` snapshot, closing the loop between 3.1.2's append-only edge log and 3.1.1's read-optimized
adjacency format. The path to a clean implementation was unusually long: two real, compounding correctness
bugs around compaction's crash-safety and retry machinery were found and fixed across three verification
passes — first, a retry-after-failure bug that permanently corrupted `ENTITY_COOCCUR` edge weights on repeated
retries; then, after fixing that, a second, more severe bug that caused **silent, permanent data loss on the
ordinary no-crash-required happy path**, found only because the verifier tested beyond the given spec. Both
are now fixed via a durable per-node compacted-through sidecar plus a durable, monotonic WAL segment-number
floor that guarantees segment numbers are never reused after truncation. No breaking API changes. One
non-blocking, pre-existing lock-ordering gap (predating this subtask, present since the very first commit at
this seam) is carried forward for a future Phase 4.5 follow-up (GitHub milestone #10, issues #38-42).

Issue #15 (Epic Phase 3) remains open: 3 subtasks remain (3.1.4-3.1.6). All three commits in this record
(`ebbc1ff`, `9850083`, `ed57468`) are local-only; no push performed this session.
