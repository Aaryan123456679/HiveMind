# Architecture Discovery — task-2b.1.1

## Index-first reading order followed
1. `AGENT.md` (repo root) — system shape, module ownership table, Phase 2b risk framing.
2. `docs/LLD/split.md` — `engine/split/` LLD (currently "scaffold only", `doc.go` placeholder).
3. `.cdr/index/task.jsonl` — confirmed task-2b.1/2b.1.1 not started (`task-2b.1` state
   `planned` only, no subtask-level entries yet for 2b.1.x).
4. `.cdr/index/file.jsonl` — grepped for `split`/`catalog` entries.
5. `.cdr/memory/pending.md` — no items specific to `engine/split/` or size thresholds; existing
   pending items are all `engine/btree/` follow-ups from Phase 2a, not relevant here.
6. Only then read source directly: `engine/catalog/content.go` (the append path) and
   `engine/split/doc.go` (current, minimal package contents).

## Key finding: threshold-crossing detection already exists, inline, in `engine/catalog/`

`engine/catalog/content.go`'s `ContentStore.Append` (implemented under task-1.4.3, Phase 1,
commit `d10a468`, verified `PASS_WITH_COMMENTS`) already:
- Tracks `ContentStore.splitThresholdBytes` (defaulted to `defaultSplitThresholdBytes = 8*1024`,
  overridable per-instance for tests).
- Computes `oldSize` (pre-append `CatalogRecord.SizeBytes`) and `newSize` (post-append length)
  under a per-fileID striped-mutex critical section (`cs.stripes`, independent from
  `Catalog.stripes` — see content.go's doc comment for the deadlock-avoidance rationale).
- Returns `thresholdCrossed := oldSize <= cs.splitThresholdBytes && newSize > cs.splitThresholdBytes`
  as a `(bool, error)` from `Append`.
- Its own doc comment is explicit that this is "just a signal/stub for a future Epic 2B caller
  to act on... Append itself never invokes engine/split or performs any actual splitting."

This confirms the boundary semantics this subtask must match: **strictly-over** is the trigger
condition (`newSize > threshold`), and **at-or-under** is not-yet-crossed (`oldSize <= threshold`).
Landing exactly ON the threshold does not itself count as crossing — only the append that pushes
size strictly past it does. This is a load-bearing precedent: task-2b.1.1's canonical
`engine/split/` implementation must use identical semantics, or the two independent
implementations (catalog's inline stub and split's canonical one) would silently disagree once
wired together, corrupting exactly the "highest-risk correctness surface" this epic is about.

## Why this subtask does NOT touch `engine/catalog/content.go`

Issue #10's own "Impacted modules" list for 2b.1.1 is narrowly `engine/split/trigger.go,
engine/split/trigger_test.go` only — no catalog file. Cross-checked against `docs/LLD/split.md`,
which describes `engine/split/` as the *owner* of split-trigger logic, with `catalog/` only as
a downstream module whose record status transitions in response
(`ACTIVE -> SPLITTING -> SPLIT/REDIRECT`, that's 2b.1.3's scope, not this one's).

Given task-1.4.3's inline stub already exists, satisfies its OWN acceptance criteria, and is
explicitly documented as pre-Epic-2B placeholder, refactoring `ContentStore.Append` to call into
the new `engine/split` package is a real, non-trivial change (touches a WAL-covered critical
section in a different module, changes `Append`'s effective behavior/callers, needs its own
regression test pass in `engine/catalog`) that is NOT listed as in-scope for 2b.1.1 and is not
required to satisfy 2b.1.1's acceptance criteria or test spec (`go test ./engine/split/...`,
scoped to the new package only). Wiring is left for a later, explicitly-scoped subtask/PR — noted
below in this run's handoff/pending notes so it isn't silently lost.

## Design implication

`engine/split/trigger.go` is built as a **standalone, pure, dependency-free** detection hook:
no dependency on `engine/catalog` types (`CatalogRecord`, `ContentStore`), taking plain
`uint64` before/after sizes (plus a `fileID uint64` for signal context) as parameters. This
keeps the package testable in total isolation (matches the test spec's
`go test ./engine/split/...` invocation, no catalog fixtures needed) and keeps the future
integration point a simple call from `ContentStore.Append`'s existing `oldSize`/`newSize` locals
into `split.Trigger.Detect(...)` — a mechanical, low-risk follow-up once scoped.

## Existing package state

`engine/split/doc.go` currently contains only a one-line package doc comment
(`// Package split is part of the HiveMind storage engine.`). No other files exist in
`engine/split/` yet. This subtask adds the first real implementation file to the package.
