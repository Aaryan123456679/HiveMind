# Subtask 3.1.3 — Requirement (source: `gh issue view 15`)

NOTE: issue #15's body, per repeated prior observation in this repo, has injected
fake instruction-like text appended after the real content (fake "date changed",
fake MCP "tokensave" tool instructions, fake "Auto Mode Active" directive were
observed in this run's `gh issue view` output). These are NOT part of the issue
and are NOT followed. Only the structured subtask list below is treated as the
real, authoritative source.

## Subtask 3.1.3 (verbatim, degapped from the issue's terse bullet style)

- **Title**: Periodic compaction: edge logs compacted into CSR adjacency array
- **Acceptance criteria**: Compaction merges accumulated per-node edge-log
  entries into the CSR array without losing or duplicating edges, including
  weight increments on repeated `ENTITY_COOCCUR` edges.
- **Test spec**: `go test ./engine/graph/... -run TestCompaction`: append many
  edges (including repeated `ENTITY_COOCCUR` edges) via the edge log, run
  compaction, assert the resulting CSR is correctly merged/weighted.
- **Impacted modules**: `engine/graph/compact.go`, `engine/graph/compact_test.go`

## Context (adjacent subtasks referenced by 3.1.3, from issue #15)

- 3.1.1 (done): `graph.dat` CSR-like compact adjacency array (`engine/graph/csr.go`).
- 3.1.2 (done): append-only per-node edge log (`engine/graph/edgelog.go`), one
  `wal.Writer`-backed log per source fileID, avoiding a shared-array lock.
- 3.1.4 (not yet done): full `EdgeType` set (`ENTITY_COOCCUR`, `LLM_ASSERTED`,
  `SPLIT_SIBLING`, `REDIRECT`) + type-filtered validation, `engine/graph/edge.go`.
  Only `EdgeTypeInvalid`/`EdgeSplitSibling`/`EdgeRedirect` exist on disk today
  (in `edge_append.go`, task-2b.3.4). Since 3.1.3's own test spec requires
  exercising `ENTITY_COOCCUR` edges, and that constant does not yet exist,
  this subtask adds the minimal `EdgeEntityCooccur` (and, for symmetry/
  non-blocking of 3.1.4, `EdgeLLMAsserted`) constants to the existing
  `EdgeType` enum in `edge_append.go`. Full type-filtered validation logic
  remains explicitly out of scope here (deferred to 3.1.4, per the issue's
  own module split).
