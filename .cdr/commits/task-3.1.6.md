# task-3.1.6 — Full round-trip capstone integration test (closes issue #15, Epic Phase 3)

## Summary

Sixth and **final** subtask of GitHub issue #15 (Epic Phase 3: Graph store + ingestion
agents), complete and independently verified (PASS_WITH_COMMENTS). Adds
`engine/graph/graph_test.go`'s `TestGraphRoundTrip` — the capstone integration test for
the whole `engine/graph` package, composing all five prior subtasks'
individually-verified pieces (3.1.1 CSR format, 3.1.2 per-node edge log, 3.1.3 compaction,
3.1.4 edge-type validation, 3.1.5 BFS traversal) into one pipeline: `EdgeLog` append ->
`Compact` -> `graph.dat` -> `LoadCSR` -> `GraphNeighbors`, run across three
append/compact cycles and a genuine simulated process restart, and checked against an
independently-computed serial oracle. Test-only change; zero production code touched.
**GitHub issue #15 is closable by this commit** (issue itself not touched as part of this
documentation step — see Verification and closing note below).

## Features

- `engine/graph/graph_test.go` (new): `TestGraphRoundTrip`, appending edges via `EdgeLog`
  across all four `EdgeType` values (`ENTITY_COOCCUR`, `LLM_ASSERTED`, `SPLIT_SIBLING`,
  `REDIRECT`) and multiple source fileIDs, running `Compact`, and verifying `GraphNeighbors`
  traversal results.
- Three append-then-compact cycles reusing the same `EdgeLog` root and `graph.dat` path,
  deliberately re-touching already-compacted-and-truncated nodes each cycle — the exact
  seam class that produced 3.1.3's F1 (compaction-retry double-counted weight) and F2
  (WAL segment-reuse data loss) bugs, neither of which a single-cycle test could have
  caught.
- A genuine simulated process restart between cycles 2 and 3: in-memory `*CSRGraph`
  discarded, fresh `LoadCSR` from disk, fresh `OpenEdgeLog` against the same root — proves
  durability survives a restart, not just in-memory correctness within one process's
  lifetime.
- Independently-computed serial oracle: a plain map built by iterating every appended edge
  and applying the same weight-aggregation/dedup rules `mergeEdges` documents, computed
  without calling `mergeEdges` or `GraphNeighbors` internals — an honest end-to-end check,
  not a tautology against the code under test.
- No production code changed: `csr.go`, `edge.go`, `edgelog.go`, `compact.go`,
  `traverse.go` all confirmed unchanged; this subtask is additive test coverage only.

## Impact

Closes out `engine/graph`'s Epic Phase 3 build-out with a permanent regression baseline
covering the full write-then-read pipeline as a composed whole, not just as five
individually-correct pieces. This is the highest-value kind of test this phase could add:
both real bugs found during the phase (3.1.3's F1/F2) were seam bugs invisible to
single-subtask tests, and this capstone specifically re-creates that seam (multi-cycle
compaction over already-truncated nodes, plus a real restart) as its core scenario. No
wire-format, schema, or existing-API changes; no breaking changes; test-only diff.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `.cdr/runs/2026-07-08/021-verification/verification.json`
- **Commit reviewed**: `e79513f2` (parent `369fd651c` — 3.1.5's doc-fix follow-up)
- Verification independently read the actual test code (not just the diff summary) and
  confirmed: the three-cycle structure genuinely re-touches compacted-and-truncated nodes;
  the process-restart simulation is real (fresh `LoadCSR`/`OpenEdgeLog`, old `*CSRGraph`
  discarded, not just a variable reassignment); the oracle is computed independently of
  `mergeEdges`/`GraphNeighbors` internals; all four edge types and multiple source fileIDs
  are exercised.
- `go test ./engine/graph/... -run TestGraphRoundTrip -race -count=10`: no flake across 10
  repeats. Full `engine/graph` package suite green under `-race`. Full workspace suite
  green except the pre-existing, already-tracked `engine/split` `TestReaderDuringSplit`
  flake (unrelated to this subtask, disclosed in prior subtask docs).
- Zero must-fix findings. One non-blocking finding, already logged to
  `.cdr/index/regression.jsonl`: `.cdr/index/task.jsonl` was missing an entry for
  `task-3.1.2` (unlike 3.1.1/3.1.3/3.1.4/3.1.5, which all have entries) — a tracking-index
  gap only, `task-3.1.2` itself was independently verified PASS_WITH_COMMENTS at the time
  (see `.cdr/commits/task-3.1.2.md`, commits `32b8042`/`12eca06`,
  `.cdr/runs/2026-07-08/005-verification/`). Backfilled into `.cdr/index/task.jsonl` as
  part of this close-out (see below); no impact on this subtask's own verdict.
- Confidence: HIGH.

### Security note

Per the standing disclosure pattern for this issue (present in task-3.1.1 through
task-3.1.5's commit docs and 021-verification's own record): tool/command output
encountered during this commit-documentation step contained embedded fake
system-reminder-style prompt-injection text (a fake "date changed" notice, a fake MCP
"tokensave" server instruction block, and a fake "Auto Mode Active" directive). All were
treated as untrusted, inert data — nothing in them was acted on, and no permission,
scope, or instruction change was accepted from them.

## Release Notes

Adds `TestGraphRoundTrip`, a capstone integration test composing `engine/graph`'s full
write/compact/read pipeline (`EdgeLog` append -> `Compact` -> `graph.dat` -> `LoadCSR` ->
`GraphNeighbors`) across three append/compact cycles and a simulated process restart,
checked against an independently-computed serial oracle. Test-only, zero production code
changes, no breaking changes.

**This is the final subtask of GitHub issue #15.** All six subtasks (3.1.1-3.1.6) are now
implemented and independently verified PASS/PASS_WITH_COMMENTS. Implementation commit
`e79513f2ab7ec1752ea817ce9e1a309c1e6e9ede` (parent `369fd651c16dd7e92eb939abe943c341ee2d5b6e`)
is local-only, not pushed — no push performed as part of this commit-documentation step.
**GitHub issue #15 itself was NOT closed or otherwise touched as part of this step** — that
requires separate explicit user authorization, per standing instruction; this record only
establishes that the issue is *closable*, as reference documentation for whoever performs
that GitHub-side action next.

---

## Issue #15 closure summary (Epic Phase 3: Graph store, `engine/graph`)

All six subtasks of issue #15 are complete and independently verified. Full arc:

1. **3.1.1 — CSR adjacency format** (`csr.go`, commit `a4e473d71`): compact, reloadable
   compressed-sparse-row on-disk format (`graph.dat`) for per-node adjacency lists — the
   durable read side every later subtask builds on.
2. **3.1.2 — Per-node edge log** (`edgelog.go`, commits `32b8042`/`12eca06`): append-only,
   fsync-per-write, per-source-fileID `EdgeLog` write path ahead of compaction, so
   concurrent writers touching different fileIDs don't contend on one shared lock.
3. **3.1.3 — Compaction** (`compact.go`, commit `ed5746834`): merges `EdgeLog` entries into
   `graph.dat`'s CSR array. Notable two-bug arc found and fixed during verification: **F1**
   (compaction-retry after a failed truncation re-summed already-merged log entries,
   compounding `ENTITY_COOCCUR` weight on each retry) and **F2** (WAL segment-number reuse
   after `TruncateNode` caused silent, permanent data loss) — both seam bugs only
   reproducible across *repeated* compaction cycles over the *same* node, which directly
   motivated 3.1.6's capstone design.
4. **3.1.4 — Edge-type validation** (commit `4b9c63919`): closed a gap 3.1.3 explicitly
   deferred — undefined `EdgeType` byte values are now rejected at every entry point
   (edge-log append, CSR encode, CSR decode) instead of being silently persisted and
   round-tripped.
5. **3.1.5 — `GraphNeighbors` traversal API** (`traverse.go`, commit `e837f1bc6`, doc-fix
   follow-up `369fd651c`): bounded (0-2 hop) BFS query API over the compacted CSR graph,
   with edge-type-filtered traversal-time pruning and hop/weight/target-ranked result
   capping — the first working read entry point the query agent can call.
6. **3.1.6 — Round-trip capstone test** (this record, commit `e79513f2a`): proves the
   whole pipeline composed together, across multiple compaction cycles and a real process
   restart, matches an independently-computed oracle. Specifically re-exercises the F1/F2
   seam class as its core scenario, closing the loop on 3.1.3's bug history.

**Net result**: `engine/graph` now has a durable, restart-safe, concurrently-writable graph
store — append path (3.1.2) feeding validated (3.1.4) compaction (3.1.3) into a compact
on-disk read format (3.1.1), with a bounded traversal query API (3.1.5) on top, and a
capstone integration test (3.1.6) verifying the whole thing end-to-end including the exact
seam class that produced this phase's two real bugs. No breaking changes across the whole
issue; all changes additive to `engine/graph`. All six implementation commits
(`a4e473d71`, `32b8042`/`12eca06`, `ed5746834`, `4b9c63919`, `e837f1bc6`/`369fd651c`,
`e79513f2a`) are local-only, not pushed.

**Issue #15 is closable** on the strength of this record. Per task instruction, **the
GitHub issue itself has NOT been closed or otherwise touched** as part of this
commit-documentation step — this is reference documentation only, pending separate
explicit user authorization for the GitHub-side action.
