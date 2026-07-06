# Requirement — task-2b.1.1

Source: GitHub issue #10 ("[2b] Split trigger + per-file CAS guard (engine/split/)"), subtask 2b.1.1,
first of 3 subtasks under task-2b.1 (parent epic Phase 2b: Auto-split, `engine/split/` —
flagged in AGENT.md as the highest-risk correctness surface in the whole engine).

## Subtask 2b.1.1 — Size-threshold detection hook on the append path

**Acceptance criteria**: An append that pushes a file's sizeBytes over the configured threshold
(default ~8KB / ~2000 tokens, tunable) triggers exactly one split-eligibility signal; appends
that stay under threshold trigger none.

**Test spec**: `go test ./engine/split/... -run TestThresholdDetection` — append content
crossing/not-crossing the threshold in various increments, assert signal fires only on the
crossing append.

**Impacted modules** (per issue #10, narrowly scoped to this subtask only):
`engine/split/trigger.go`, `engine/split/trigger_test.go`.

Explicitly OUT of scope for this subtask (later subtasks/tasks per issue #10 and the wider
epic): `engine/split/guard.go` (CAS guard, 2b.1.2), `engine/split/orchestrate.go` (SPLITTING
status transition, 2b.1.3), actual split execution / `ProposeSplit` RPC wiring (issues #11-14),
and wiring this hook into `engine/catalog`'s existing `ContentStore.Append` call site (not listed
as an impacted module for 2b.1.1 — see architecture-discovery.md for why this is deliberately
deferred, not an oversight).

## Correctness properties (from task brief, elevated rigor per AGENT.md's risk framing)

Exactly one signal per crossing, not one signal per append-while-over-threshold:
1. Append stays under threshold -> no signal.
2. Append exactly reaches or crosses threshold -> exactly one signal.
3. Subsequent append when already over threshold -> no re-signal.
4. Multiple small appends that individually stay under but cumulatively cross -> signal fires
   on whichever specific append causes the crossing, and only that one.

Edge cases to cover explicitly:
- Zero-byte append.
- Append that lands precisely ON the threshold boundary (does landing exactly ON count as
  "crossing"? Decision: NO — must be strictly over; see plan.md for boundary semantics and
  why this matches the existing `engine/catalog/content.go` precedent).
- Threshold of 0 or negative — invalid config, must be guarded against (constructor returns an
  error, not a panic, matching this repo's `OpenContentStore`-style error-return convention).
- File that starts already over threshold at hook "install" time — must NOT retroactively
  signal; only the actual crossing append fires.
