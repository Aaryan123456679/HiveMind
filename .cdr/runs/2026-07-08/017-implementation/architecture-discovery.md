# Architecture Discovery — Subtask 3.1.5

## Index/handoff order followed
1. `.cdr/runs/2026-07-08/014-implementation/` through `016-cdr-commit/` (3.1.4's full
   artifact trail) — confirms `edge.go`'s `ValidEdgeType`/`EdgeTypeName`/`ParseEdgeType`
   were added specifically "ahead of subtask 3.1.5's `GraphNeighbors` `edgeTypeFilter`
   parameter" (edge.go's own `ParseEdgeType` doc comment, confirmed by direct read below).
2. `docs/LLD/graph.md` (full file, read via the `Read` tool directly — the `gh`/grep-piped
   copy is garbled by upstream rendering in this environment, dropping words mid-sentence;
   the file on disk is not).
3. Direct source reads (required — this subtask is a new read-path consumer spanning all
   three prior files' interfaces): `engine/graph/csr.go` (full), `engine/graph/edge.go`
   (full), `engine/graph/edgelog.go` (full).

## What already exists (confirmed by direct reading)

- `csr.go`: `CSRGraph` — in-memory, immutable-once-built adjacency index. `Neighbors
  (fileID uint64) []CSREdge` already does exactly a 1-hop lookup (binary search over
  sorted `nodeIDs`, slice of the flat `edges` array via the `offsets` array), returning a
  defensive copy, `nil` if `fileID` has no adjacency entry. This is the only lookup
  primitive `GraphNeighbors` needs to build multi-hop BFS on top of — no new CSR-side
  method is needed.
- `edge.go` (3.1.4, already shipped): `EdgeType` validity (`ValidEdgeType`), canonical
  string names (`EdgeTypeName`), and `ParseEdgeType` (inverse of `EdgeTypeName`) — the
  `ParseEdgeType` doc comment explicitly names this subtask ("provided ahead of subtask
  3.1.5's `GraphNeighbors` `edgeTypeFilter` parameter... will need to parse a
  caller-supplied type filter into an `EdgeType`"). `EdgeTypeInvalid` is the reserved
  zero-value sentinel (from `edge_append.go`), already used elsewhere in the package to
  mean "no valid type" — reused here (see plan.md) as the sentinel for "no type filter,
  match all types" in `GraphNeighbors`' `edgeTypeFilter` parameter, since 0 can never be a
  real, valid `EdgeType` value.
- `edgelog.go`: `EdgeLog`/`ReadNode`/`ReadNodeAfter` — per-node-only lookup, no cross-node
  enumeration, explicitly "not a general query API" (see requirement.md's design-question
  section for the full analysis of why this subtask does NOT read from `EdgeLog`).

## What is genuinely new work for 3.1.5

1. `traverse.go`: `GraphNeighbors(g *CSRGraph, fileID uint64, depth int, edgeTypeFilter
   EdgeType, maxNodes int) ([]CSREdge, error)` — BFS over `g.Neighbors` up to `depth` hops
   (validated to be in `[0, 2]`, per LLD's "0-2 hop traversal"), filtering each candidate
   edge by `edgeTypeFilter` (`EdgeTypeInvalid` == no filter, all 4 types match; any other
   value must be one of the 4 `ValidEdgeType` values or `GraphNeighbors` returns an error
   rather than silently matching nothing), deduplicating a node reached via multiple paths
   (keep the closest hop / first-seen, matching standard BFS dedup semantics — never
   revisit `fileID` itself, never double-count a node reached via two different 1-hop
   edges), and capping the final result at `maxNodes` entries.
2. Cap/ordering semantics (not pinned down verbatim by the issue's garbled text, resolved
   by design — see plan.md): result ordered by hop-distance ascending (closer nodes are
   more relevant to the query-agent's "expand topics" use case named in the LLD), then by
   `Weight` descending within the same hop (a stronger `ENTITY_COOCCUR` signal outranks a
   weaker one at equal hop-distance — the only field in `CSREdge` carrying a
   relevance/strength signal at all), then by `Target` fileID ascending as a final,
   deterministic tie-break (required for reproducible test assertions and stable
   query-agent behavior). Truncate to `maxNodes` after this sort. This is documented in
   `traverse.go`'s doc comment as the precise, callable-relied-upon contract, since the
   issue text alone under-specifies it.
3. Boundary conditions the task explicitly calls out, given this package's 2-severe-bug
   history at compaction/edge-log seams: cap == 0 (returns empty slice, not an error, not
   nil-vs-empty ambiguity — see plan.md), cap == exactly the number of reachable nodes (no
   truncation, no off-by-one drop), cap == reachable-1 (drops exactly one, the
   lowest-ranked by the sort order above), type filter matching no edges (empty result, no
   error), type filter matching a single type among a mixed-type graph, and depth == 0
   (must return no neighbors at all — `fileID` itself is never included in results;
   "0-hop" is a valid, if degenerate, input distinguishing it from `depth < 0` or `depth >
   2`, both of which are rejected as errors).

## Files read directly (line ranges)
- `engine/graph/csr.go:114-167` (`CSRGraph`, `Neighbors`)
- `engine/graph/edge.go:1-92` (full file — `ValidEdgeType`, `EdgeTypeName`,
  `ParseEdgeType`, `NewCSREdge`)
- `engine/graph/edgelog.go:1-192` (package doc, `EdgeLog`, `AppendEdge`, `ReadNode`,
  `ReadNodeAfter` doc comments — confirms no cross-node enumeration primitive exists)
- `docs/LLD/graph.md` (full file, 140 lines, read via `Read` tool — not the garbled
  `gh`/grep-piped copy)
- `.cdr/runs/2026-07-08/014-implementation/{requirement,architecture-discovery,handoff}`
  and `.cdr/runs/2026-07-08/016-cdr-commit/handoff.json` (3.1.4's provenance trail,
  confirms real final commit `4b9c639`, not the corrupted-looking
  `4b9c63919a7bf56f3dec431bac5ff3933391b620` string that actually is just the correct
  40-char SHA — verified via `git rev-parse HEAD`, no stale-hash bug present to repeat).
