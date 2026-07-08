# task-3.1.5 — GraphNeighbors BFS traversal API (issue #15, Epic Phase 3)

## Summary

Subtask 3.1.5 of GitHub issue #15 (Epic Phase 3) is complete and independently verified
(PASS_WITH_COMMENTS). Adds `engine/graph`'s query-time, read-only multi-hop neighbor
traversal entry point — `GraphNeighbors(g *CSRGraph, fileID uint64, depth int,
edgeTypeFilter EdgeType, maxNodes int) ([]CSREdge, error)` — a bounded (0-2 hop) BFS over
3.1.1's compacted CSR adjacency structure (`graph.dat`), used by the query agent to expand
topics when the query-time topic selector judges the initial candidate set insufficient
alone. Issue #15 stays open: 1 subtask remains (3.1.6).

A small doc-only follow-up commit (`369fd65`) was made as part of this close-out to correct
a factual mischaracterization of `docs/LLD/graph.md`'s document structure found during
verification (see Verification below) — no logic changed.

## Features

- `engine/graph/traverse.go` (new): `GraphNeighbors`, BFS traversal over `*CSRGraph`
  bounded to depth 0-2, matching `docs/LLD/graph.md`'s "Traversal API" section
  (lines 111-116) signature and hop semantics exactly.
- `edgeTypeFilter` prunes the traversal itself (edges of a non-matching type are never
  followed to enqueue their target), not just the returned result set — a deliberate
  judgment call disclosed in the doc comment: the LLD is silent on this specific question,
  and traversal-time pruning is the more coherent behavior for the topic-expansion use
  case this API serves.
- Dedup by first-seen hop (BFS frontier-by-hop processing order guarantees a node reached
  at multiple hop distances is recorded at its shortest one).
- Results sorted `(hop asc, Weight desc, Target asc)`, with `maxNodes` cap applied *after*
  sorting, so truncation always keeps the best-ranked candidates rather than an arbitrary
  BFS-order prefix.
- Strict input validation: `depth` outside `[0,2]`, `edgeTypeFilter` neither a
  `ValidEdgeType` nor the "all types" sentinel, and negative `maxNodes` are all rejected
  before any traversal runs; `depth == 0` is a distinct, valid "empty result" case.
- Read-only: only calls `CSRGraph.Neighbors` (binary search + defensive copy); no mutation
  of `*CSRGraph` or any durable state. `csr.go`, `edge.go`, `edgelog.go`, `compact.go` all
  confirmed unchanged.
- `engine/graph/traverse_test.go` (new): 14 subtests, including the issue's literal
  reachable-nodes-exceeds-maxNodes-within-2-hops scenario, cap-exact/cap-plus-one/cap-zero,
  diamond dedup, all four edge-type filter values plus the invalid sentinel, and every
  invalid-input rejection path.

## Impact

Purely additive: adds one new file pair to `engine/graph`, no changes to any existing
production file in the package. Gives the query agent its first working multi-hop context
expansion primitive over the compacted graph, gated entirely behind caller-supplied
`depth`/`maxNodes` bounds (the system-wide cap policy itself remains a query-agent concern,
per `docs/LLD/query-agent.md`, not decided in this package). No wire-format, schema, or
existing-API changes; no breaking changes.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `.cdr/runs/2026-07-08/018-verification/verification.json`
- Zero blocking findings. All dimensions pass or pass-with-non-blocking-comment:
  correct depth-boundary behavior (no off-by-one), correct dedup, cap truncation confirmed
  to happen after sort (best candidates retained), confirmed read-only, correct input
  validation, thorough test coverage (14 subtests, independently confirmed present and
  substantive, not placeholders), zero regressions (`engine/graph` package diff limited to
  the two new files), full workspace suite green including `-race`.
- One low-severity, now-fixed finding: `traverse.go`'s doc comment incorrectly stated the
  LLD's "Traversal API" section (line 111) "sits under" "Storage layout" (line 15) — the
  two are in fact sibling top-level sections, separated by an intervening "Edge shape"
  section (line 97). The underlying design conclusion (EdgeLog is never mentioned in the
  Traversal API section, so compacted-only reads are a defensible interpretation) was
  correct and unchanged; only the document-structure claim was wrong. Corrected directly in
  follow-up commit `369fd65` (doc-comment-only, no logic change, re-verified via
  `gofmt -l`/`go build`/`go vet`/`go test ./graph/... -run TestGraphNeighbors`, all clean).
- One low-severity, non-blocking, no-action-needed finding: no single dedicated subtest
  covers a node reachable at hop 1 directly *and* at hop 2 via a different, longer path
  (only hop-2-vs-hop-2 diamond dedup is directly tested); correctness here follows by
  construction from the frontier-by-hop BFS order, not from an untested code path. Not
  added to `pending.md` — verification treated this as optional/no-action, not a tracked
  gap.
- Confidence: HIGH.

## Release Notes

Adds `GraphNeighbors`, a bounded (0-2 hop) breadth-first traversal API over `engine/graph`'s
compacted `CSRGraph`, with optional edge-type filtering (pruned at traversal time),
first-seen-hop dedup, and `(hop, weight, target)`-ranked result capping. Purely additive,
read-only, no breaking changes. Includes a small documentation fix correcting an inaccurate
description of `docs/LLD/graph.md`'s section structure in the new file's doc comment
(commit `369fd65`); no logic changed.

**This does not close GitHub issue #15.** Subtasks 3.1.1-3.1.5 are done; 1 subtask
(3.1.6) remains before Epic Phase 3's issue #15 is closable. Implementation commit
`e837f1b` and doc-fix follow-up `369fd65` are local-only, not pushed — no push performed as
part of this commit-documentation step.
