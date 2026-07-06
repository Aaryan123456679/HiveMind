# task-2b.1.1 — Size-threshold detection hook (subtask 1 of 3, issue #10)

## Summary
First of three subtasks under GitHub issue #10 ("[2b] Split trigger + per-file CAS guard", Epic Phase 2b: Auto-split). Adds `engine/split/trigger.go`: a stateless, tunable size-threshold detection hook (`Trigger`/`Signal`/`CrossesThreshold`) that fires exactly one split-eligibility signal on the append that crosses the configured byte threshold (default 8KB), and stays silent for appends that remain under threshold or are already over it. Boundary semantics are matched to (but deliberately not wired into) `engine/catalog/content.go`'s pre-existing task-1.4.3 inline `thresholdCrossed` stub.

## Features
- `engine/split.Trigger` / `Signal` / `CrossesThreshold`: pure, stateless exactly-once-per-crossing detection over caller-supplied (oldSizeBytes, newSizeBytes) pairs.
- Tunable threshold via `NewTrigger(thresholdBytes)`, with `DefaultThresholdBytes` (8*1024) matching `engine/catalog`'s existing default.
- Rejects invalid (zero) thresholds via error return rather than panic.
- `TestThresholdDetection` (8 subtests) covers all 4 acceptance-criteria scenarios plus edge cases: zero-byte appends, exact-boundary landing (strictly-over required), already-over-threshold no-resignal, and defensive shrinking-size handling.

## Impact
- This closes out **only subtask 2b.1.1** of issue #10. Two more subtasks remain open under the same issue: **2b.1.2** (per-file CAS guard, `engine/split/guard.go`) and **2b.1.3** (catalog `SPLITTING` status transition, `engine/split/orchestrate.go`). Issue #10 as a whole is not yet complete.
- Deliberately does not wire `Trigger` into `engine/catalog/content.go`'s `ContentStore.Append` call site — out of scope per issue #10's impacted-modules list for 2b.1.1 (by design, not oversight; see `.cdr/runs/2026-07-06/017-implementation/architecture-discovery.md`).
- Carried-forward non-blocking finding (tracked in `.cdr/memory/pending.md`): two independent, currently-matching implementations of the same threshold-crossing boundary logic now exist — `engine/split/trigger.go`'s `CrossesThreshold` and `engine/catalog/content.go`'s pre-existing inline `thresholdCrossed` stub — with no shared source of truth or drift-detection test yet. Low risk today (correctly scoped out of this subtask), but should not persist past 2b.1.2/2b.1.3; recommendation is to wire the two together as part of (or immediately after) 2b.1.2's CAS guard lands.
- No regression: `engine/catalog`'s `Append` (lines 235-281) confirmed unmodified; full `go test ./catalog/...` green.

## Verification
- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: 2026-07-06-018-verification
- **Details**: All 9 dimensions passed (8 `pass`, 1 `pass_with_comment` on maintainability — statelessness pushes before/after-size correctness burden onto the caller, honestly documented rather than silently assumed). Zero blocking findings. One non-blocking finding (the two-independent-implementations drift risk noted above, already correctly reflected in `pending.md`). Confidence: high — verifier read full `trigger.go`/`trigger_test.go`, cross-checked against `engine/catalog/content.go`'s actual (not claimed) implementation, and re-ran all specified tests plus build/vet/gofmt gates, all green with zero fixes needed. Commits reviewed: `9214a83` (feat), `a7eaf44` (docs/cdr).

## Release Notes
`engine/split` package gains its first capability: a standalone, well-tested size-threshold detection hook (`Trigger`) that signals exactly once when an append crosses a configurable byte threshold (default 8KB). This is subtask 1 of 3 toward full auto-split support (issue #10); the CAS guard (2b.1.2) and catalog `SPLITTING` status wiring (2b.1.3) are still to come before this hook is connected to the live append path. No breaking API change; new package surface only.
