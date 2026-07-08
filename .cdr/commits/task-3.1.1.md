# task-3.1.1 — CSR-like compact adjacency array format for graph.dat (issue #15, Epic Phase 3)

## Summary

Subtask 3.1.1 of GitHub issue #15 (Epic Phase 3) is complete and independently verified
(PASS_WITH_COMMENTS). It adds `engine/graph`'s first compact, reloadable adjacency-array persistence
format for `graph.dat` — a CSR (compressed sparse row) representation, alongside the existing append-only
edge log (`edge_append.go`) added in Epic Phase 2b. This is the durable, restart-safe adjacency store that
the remaining Epic Phase 3 subtasks (3.1.2 edge log, 3.1.3 compaction, 3.1.5 traversal API) build on.

Issue #15 stays open: 5 more subtasks remain (3.1.2-3.1.6). This document covers only 3.1.1.

## Features

- CSR on-disk format for `graph.dat`: 28-byte header (magic, version, node/edge counts, CRC32 of payload)
  followed by a sorted node-ID array, a CSR offsets array, and a flat fixed-width edge array (Target, Type,
  Weight, LastUpdated).
- Atomic whole-file rewrite (temp-file + fsync + rename), mirroring `engine/catalog/content.go`'s existing
  durable-write precedent — appropriate here since CSR arrays are always rebuilt wholesale by future
  compaction, never incrementally appended.
- Load path validates magic/version/CRC and rejects truncated or corrupted files rather than silently
  returning bad data.
- In-memory adjacency map giving O(log n) per-node neighbor lookup.
- 5 new tests covering round-trip fidelity, empty-graph, corruption detection, truncated-header, and
  large-adjacency edge cases.
- `docs/LLD/graph.md` updated to document the on-disk CSR layout.

## Impact

- Zero changes to the existing append-only edge log (`edge_append.go`) or any other package; this is a
  purely additive persistence primitive scoped strictly to `engine/graph`.
- Verification independently confirmed: correct round-trip fidelity at byte/field level, correct CSR
  offset-array math (no off-by-one errors), genuine corruption/truncation rejection (not merely
  happy-path tests), and zero regressions to `edge_append_test.go`.
- The one full-suite failure observed during verification (`engine/split.TestReaderDuringSplit`) was
  independently reconfirmed as the pre-existing, disclosed ~1-3% timing flake already tracked in
  `.cdr/memory/pending.md` and `.cdr/index/regression.jsonl` — not a new regression. This was confirmed
  structurally via `go list -deps`, which shows no dependency from `engine/graph` to `engine/split`, so the
  two are provably unrelated.
- Two non-blocking findings from verification, both newly logged in `.cdr/memory/pending.md`:
  1. `LoadCSR`/`decodeCSREdge` don't validate the on-disk `EdgeType` byte against known values (unlike
     `edge_append.go`'s `decodeEdge`). Low risk today since there is a single writer, but worth revisiting
     once 3.1.2/3.1.3 add a second write path into `graph.dat`.
  2. The pre-existing `TestReaderDuringSplit` flake reproduced once during the full-suite run, as expected —
     already tracked, no new entry needed for this occurrence.
- Both items above are tracked as prospective tech-debt for eventual folding into GitHub milestone #10
  ("Phase 4.5: Storage-engine technical debt & correctness follow-ups", issues #38-42) at Phase 3's close-out,
  per standing convention — no new GitHub issues created directly for them now.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run**: `.cdr/runs/2026-07-08/002-verification/verification.json`
- **Commit verified**: `a4e473d71402e46f9cfa69ba37c8743cf68af059`
- Zero blocking findings (`blocking_findings: []`, `proceed_to_commit: true`, `safe_to_proceed_to_3.1.2: true`
  per the verification record). Two non-blocking findings, both recorded above and in `.cdr/memory/pending.md`.

## Release Notes

Adds a CSR (compressed sparse row) compact adjacency-array format for `graph.dat`, providing `engine/graph`'s
first durable, restart-safe, reloadable adjacency representation — written atomically via temp-file +
fsync + rename, with corruption/truncation detection on load. This is additive: the existing append-only edge
log is untouched. No breaking API changes. Lays the persistence foundation for the upcoming edge log (3.1.2),
compaction (3.1.3), and traversal API (3.1.5) subtasks of issue #15.

**This does not close GitHub issue #15.** Only subtask 3.1.1 is done; 5 more subtasks (3.1.2-3.1.6) remain
before Epic Phase 3's issue #15 is closable. Implementation commit `a4e473d` is local-only, not yet pushed —
no push performed as part of this commit-documentation step.
