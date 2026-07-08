# Architecture discovery — subtask 3.1.6

## Sources read (in order, per token-order protocol)
- `.cdr/commits/task-3.1.1.md` through `task-3.1.5.md` (full component history,
  including 3.1.3's two-bug fix arc: F1 retry double-counting, F2 silent permanent
  data loss via WAL segment-number reuse — both at the compaction seam).
- `docs/LLD/graph.md` (storage layout, per-node edge log, compaction crash-safety
  ordering and weight-aggregation semantics, edge shape, traversal API section).
- Direct source reads (necessitated by this subtask's whole-package-integration
  nature, per the dispatch instructions): `engine/graph/csr.go`, `edgelog.go`,
  `compact.go`, `edge.go`, `traverse.go`, `edge_append.go` (EdgeType constants),
  `doc.go`, and all existing `*_test.go` files' `func Test...` signatures to confirm
  no name collision and to match existing test-file conventions (e.g.
  `sortedNeighbors` helper in `compact_test.go`).

## Composed pipeline (what 3.1.6 must exercise end-to-end)

```
EdgeLog.AppendEdge (3.1.2, per-node WAL)
        |
        v
Compact(graphPath, log) (3.1.3: merge with existing graph.dat + write via WriteCSR,
        |               then TruncateNode per compacted node)
        v
graph.dat (3.1.1 CSR format, on-disk, atomically written)
        |
        v
LoadCSR / *CSRGraph (3.1.1, in-memory read-optimized index)
        |
        v
GraphNeighbors (3.1.5, BFS traversal, edge-type filter (3.1.4), maxNodes cap)
```

Key composition facts that drive the test design:
- `Compact` returns a `*CSRGraph` already built in-memory (no reload needed to use it
  immediately) — but a genuine "full round trip" / durability proof requires also
  calling `LoadCSR` fresh against the same path, since `Compact`'s returned in-memory
  object is not proof the bytes on disk are correct/reloadable.
- `mergeEdges` (compact.go) semantics: `EdgeEntityCooccur` sums Weight across every
  occurrence (existing CSR entry + all incoming log entries for that (source,target)),
  LastUpdated takes the max. Every other type (`SPLIT_SIBLING`, `REDIRECT`,
  `LLM_ASSERTED`) is deduplicated by (target, type) with most-recent-LastUpdated
  winning outright (never summed). The test's oracle must mirror this exactly,
  computed independently (plain map iteration over the full history of appended
  edges), not by calling `mergeEdges` itself.
- `GraphNeighbors`'s dedup/ranking: BFS frontier-by-hop (first-seen-hop wins),
  sorted (hop asc, Weight desc, Target asc), maxNodes cap applied after sort.
  `edgeTypeFilter` prunes traversal itself (edges of non-matching type are never
  followed), not just the result set — the oracle's traversal-simulation must match
  this pruning behavior, not naive "filter after full BFS".
- `EdgeLog.TruncateNode` no longer deletes a node's directory (F2 fix) — it keeps the
  directory around holding a `wal.WriteSegmentFloor` marker. A second/third
  append+compact cycle against the SAME `EdgeLog`/root after a prior compaction is
  exactly the regime F1/F2 were found in, so the test exercises this directly by
  reusing the same `*EdgeLog` (and, for the post-restart cycle, a freshly re-opened
  `EdgeLog` against the same root, mirroring a real process restart) across all three
  append+compact cycles.
- `Compact`'s crash-safety doc comment establishes "post-rename graph.dat is
  authoritative regardless of truncation outcome" as this package's posture, and
  `traverse.go`'s doc comment confirms `GraphNeighbors` is deliberately
  compacted-only (never reads EdgeLog directly) — so the test's simulated-restart
  step only needs to reload `graph.dat` (via `LoadCSR`), not the EdgeLog, to prove
  read-path durability across a restart.

## Reused test infra
- `sortedNeighbors` (compact_test.go) — reusable in this new file for
  order-independent comparisons if needed, but `GraphNeighbors`'s own output is
  already deterministically sorted, so the oracle comparison in this test instead
  independently sorts its own oracle-derived expected slice using the same
  (hop, weight desc, target) order `GraphNeighbors` documents, and compares directly.
- `NewCSREdge`/`ParseEdgeType`/`EdgeTypeName` (edge.go, 3.1.4) — used to construct
  valid, canonically-typed edges without literal EdgeType byte values.
- `OpenEdgeLog`, `WriteCSR`/`LoadCSR`, `Compact`, `GraphNeighbors` — the four public
  composition points this test drives directly, exactly as a real caller (ingestion
  agent -> compaction job -> query agent) would.

## No production code changes anticipated
This subtask's issue text explicitly scopes only `engine/graph/graph_test.go` (new
file) as the impacted module — consistent with 3.1.6 being a pure correctness-proof
test subtask over already-implemented (3.1.1-3.1.5) production code, not a new
feature. Confirmed no gaps requiring a production fix were found while designing the
oracle (mergeEdges/GraphNeighbors semantics as documented are self-consistent and
implementable by an independent oracle).
