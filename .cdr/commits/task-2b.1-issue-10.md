# task-2b.1 — Split trigger + per-file CAS guard (issue #10, CLOSED)

## Summary
Issue #10 ("[2b] Split trigger + per-file CAS guard", Epic Phase 2b: Auto-split) is complete: all 3 subtasks implemented and independently verified. Together they deliver the full **detect → guard → status-transition** primitive chain for auto-split:
- **2b.1.1** (`engine/split/trigger.go`, commit `9214a83`): stateless `Trigger`/`Signal`/`CrossesThreshold` — fires exactly once when an append crosses a configurable byte threshold (default 8KB).
- **2b.1.2** (`engine/split/guard.go`, commit `bed7edd`): `FileGuard` — per-fileID, atomic-CAS-backed `splitInProgress` flag, guaranteeing exactly one caller wins split-initiation per threshold crossing (closes the TOCTOU class explicitly called out in the issue's acceptance criteria).
- **2b.1.3** (`engine/split/orchestrate.go`, commit `124490b`): `Orchestrator` — composes `FileGuard` with `CatalogRecord.Status` transitions (`BeginSplit`/`EndSplit`/`AbortSplit`/`AdmitWrite`), marking a file `SPLITTING`, refusing new writers, and proving existing MVCC reader snapshots are unaffected.

## Deliberate Scope Boundary vs Issue #12
Issue #10 provides **only** the detect → guard → status-transition primitives. It explicitly does **not** cover:
- Allocating new fileIDs, writing new content files, or writing redirect stubs.
- Updating the catalog to `StatusSplit`/`StatusRedirect` with real `RedirectTargetIDs`.
- B+Tree repointing/graph wiring.
- The single atomic, WAL-covered commit of a full split, or releasing writers queued during the split.

All of the above is issue #12's scope ("Atomic split-transaction execution", subtasks 2b.3.1-2b.3.6). Issue #10's `Orchestrator.EndSplit`/`AbortSplit` exit primitive is a clean, outcome-parameterized hook (`StatusActive` on abort, forward on success) that #12 will call once it has a real `RedirectTargetIDs` to transition to — issue #10 does not attempt to anticipate or stub that transition itself.

## Impact / Known Follow-ups
Two non-blocking gaps are now tracked in `.cdr/memory/pending.md` for issue #12 (or a dedicated follow-up) to pick up:
1. **Two independent threshold-crossing implementations**: `engine/split/trigger.go`'s `CrossesThreshold` (2b.1.1) and `engine/catalog/content.go`'s pre-existing task-1.4.3 inline `thresholdCrossed` stub currently match but have no shared source of truth or drift-detection test. `Trigger` was deliberately not wired into `ContentStore.Append` in this issue's scope.
2. **Abandoned `SPLITTING` record on crash has no automatic recovery**: if a split holder crashes between `BeginSplit` and `EndSplit`/`AbortSplit` (2b.1.3), the catalog record is permanently stuck `StatusSplitting` with no timeout/recovery path. Explicitly deferred because no split executor exists yet to make crash-injection/recovery testing meaningful — issue #12 should decide on a recovery story (e.g. lease/heartbeat timeout, or an explicit repair pass) before wiring a real executor to `BeginSplit`/`AdmitWrite`.

No regressions across the three subtasks: `engine/btree`, `engine/mvcc`, `engine/catalog` all remain green throughout.

## Verification
- **2b.1.1**: PASS_WITH_COMMENTS, run `2026-07-06-018-verification`.
- **2b.1.2**: PASS, run `2026-07-07-005-verification`.
- **2b.1.3**: PASS_WITH_COMMENTS, run `2026-07-07-008-verification`.
- **Overall**: Issue #10 closable — all 3 subtasks verified, zero must-fix findings across the issue, 2 non-blocking follow-ups tracked for issue #12.

## Release Notes
Issue #10 delivers the complete "split trigger + per-file CAS guard" feature: size-threshold detection, a CAS-backed per-file guard against duplicate split initiation, and catalog-level `SPLITTING` status orchestration with proven MVCC reader isolation. This is the detection/admission layer only — real split execution (new fileIDs, content/redirect writes, B+Tree repointing, queued-writer release) is issue #12's scope. No breaking API changes; `engine/split` is new package surface, not yet wired into any live append path.
