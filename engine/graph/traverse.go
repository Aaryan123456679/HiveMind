// Package graph (this file): subtask 3.1.5's read-only, multi-hop neighbor traversal API.
//
// GraphNeighbors is the query-time entry point docs/LLD/graph.md's "Traversal API" section
// names: `GraphNeighbors(fileID, depth, edgeTypeFilter, maxNodes)`, used by the engine to
// expand topics the query-time topic-selector judges insufficient alone (0-2 hop
// traversal), hard-capped at a caller-supplied maxNodes to prevent context blow-up (the
// system-wide k + 2k cap value itself is a query-agent concern - see
// docs/LLD/query-agent.md - not decided by this package).
//
// # Compacted-only design (does not merge in-flight EdgeLog entries)
//
// GraphNeighbors takes a *CSRGraph (the compacted, whole-snapshot adjacency index built by
// csr.go's BuildCSR/LoadCSR, or produced fresh by compact.go's Compact) and does not read
// from edgelog.go's EdgeLog at all. This is a deliberate design decision, not an oversight:
//
//   - docs/LLD/graph.md's "## Traversal API" section (line 111) is its own top-level
//     section, a sibling of (not nested under) "## Storage layout" (line 15) - the two are
//     separated by an intervening "## Edge shape" section. graph.dat (the CSR snapshot),
//     introduced under Storage layout, is nonetheless the thing the Traversal API section
//     describes querying; EdgeLog is never mentioned in that section.
//   - EdgeLog has no cross-node enumeration primitive (only per-fileID ReadNode/
//     ReadNodeAfter) - there is no efficient way for a BFS expansion touching many nodes to
//     also check each one's uncompacted log without a per-hop, per-node extra read against
//     a completely different storage mechanism with its own locking (EdgeLog.mu).
//   - compact.go's crash-safety story already establishes "post-rename graph.dat is
//     authoritative and durable regardless of truncation outcome" as this package's
//     posture; extending the same posture to the read path (graph.dat is the source of
//     truth queries see) is the consistent, conservative choice for a package that has
//     already had two severe bugs at compaction/edge-log seams.
//
// Consequence: an edge appended via EdgeLog.AppendEdge but not yet folded in by a Compact
// run is invisible to GraphNeighbors until the next compaction. This is an accepted,
// documented staleness window, not a correctness bug.
//
// # Result ordering and cap semantics
//
// Candidates are ordered by (hop distance ascending, Weight descending, Target fileID
// ascending) before the maxNodes cap is applied - closer nodes rank first (most relevant
// to a topic-expansion use case), then within the same hop distance a stronger
// ENTITY_COOCCUR/edge Weight signal outranks a weaker one, with Target fileID as a final
// deterministic tie-break. The maxNodes cap keeps the top-ranked maxNodes candidates by
// this order, dropping the rest.
package graph

import (
	"fmt"
	"sort"
)

// GraphNeighbors returns fileID's neighbors in g, expanded up to depth hops (0, 1, or 2 -
// "0-2 hop traversal" per docs/LLD/graph.md), optionally filtered to a single edge type,
// and capped at maxNodes results.
//
// depth must be in [0, 2]; any other value is an error. depth == 0 always returns an empty,
// nil-slice result (fileID itself is never included in the returned neighbors, even though
// it is trivially "reachable in 0 hops" from itself) - this distinguishes a valid, if
// degenerate, request from an out-of-range one.
//
// edgeTypeFilter constrains which edge types are traversed and returned. Pass
// EdgeTypeInvalid (the zero value) to mean "no filter, match every valid edge type" - this
// reuses the same sentinel edge.go's ValidEdgeType already treats as "not a real edge
// type", since 0 can never be a legitimate EdgeType value to filter *for*. Passing any
// other value that is not one of the four ValidEdgeType-recognized types is an error
// (a typo'd or otherwise undefined filter value must never silently match nothing).
//
// The filter prunes traversal, not just the final result set: an edge whose Type does not
// match edgeTypeFilter is never followed, so a node reachable only via a non-matching
// intermediate edge is unreachable under that filter, even if a matching-type edge would
// otherwise have reached it at a later hop through a different path. This mirrors "only
// follow ENTITY_COOCCUR edges" style semantics rather than "follow anything, but only show
// me ENTITY_COOCCUR edges in the result".
//
// maxNodes must be >= 0; a negative value is an error. maxNodes == 0 returns an empty, nil
// result with no error (a valid, if degenerate, request - matches g == nil's behavior
// below).
//
// g == nil is treated as "an empty graph": GraphNeighbors returns (nil, nil), not an error,
// so callers that have not yet loaded/built a graph.dat can call this without a nil check
// of their own.
//
// A node reachable via more than one path within depth hops is included exactly once, at
// the shortest hop distance it was first discovered at (standard BFS dedup semantics) -
// never duplicated, never re-ranked by a later, longer path.
func GraphNeighbors(g *CSRGraph, fileID uint64, depth int, edgeTypeFilter EdgeType, maxNodes int) ([]CSREdge, error) {
	if depth < 0 || depth > 2 {
		return nil, fmt.Errorf("graph: depth %d out of range [0, 2]", depth)
	}
	if maxNodes < 0 {
		return nil, fmt.Errorf("graph: maxNodes %d must be >= 0", maxNodes)
	}
	if edgeTypeFilter != EdgeTypeInvalid && !ValidEdgeType(edgeTypeFilter) {
		return nil, fmt.Errorf("graph: invalid edgeTypeFilter %v", edgeTypeFilter)
	}
	if g == nil || depth == 0 || maxNodes == 0 {
		return nil, nil
	}

	type candidate struct {
		edge CSREdge
		hop  int
	}

	visited := map[uint64]int{fileID: 0}
	var candidates []candidate

	frontier := []uint64{fileID}
	for hop := 1; hop <= depth; hop++ {
		var next []uint64
		for _, node := range frontier {
			for _, e := range g.Neighbors(node) {
				if edgeTypeFilter != EdgeTypeInvalid && e.Type != edgeTypeFilter {
					continue
				}
				if _, seen := visited[e.Target]; seen {
					continue
				}
				visited[e.Target] = hop
				candidates = append(candidates, candidate{edge: e, hop: hop})
				next = append(next, e.Target)
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.hop != b.hop {
			return a.hop < b.hop
		}
		if a.edge.Weight != b.edge.Weight {
			return a.edge.Weight > b.edge.Weight
		}
		return a.edge.Target < b.edge.Target
	})

	if len(candidates) > maxNodes {
		candidates = candidates[:maxNodes]
	}

	result := make([]CSREdge, len(candidates))
	for i, c := range candidates {
		result[i] = c.edge
	}
	return result, nil
}
