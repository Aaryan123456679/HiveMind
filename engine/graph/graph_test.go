// TestGraphRoundTrip is the test spec required by issue #15 subtask 3.1.6: a full
// insert/compaction/traversal round-trip correctness test exercising the entire
// engine/graph package as one composed pipeline (EdgeLog append -> Compact ->
// graph.dat -> LoadCSR -> GraphNeighbors), rather than re-testing any one component
// in isolation (each already has its own dedicated suite: TestCSRFormat/3.1.1,
// TestPerNodeEdgeLog*/3.1.2, TestCompaction*/3.1.3, TestEdgeTypes/3.1.4,
// TestGraphNeighbors/3.1.5).
//
// Unlike those component-level suites, this test deliberately targets the SEAMS
// between components, since both real bugs found this phase (F1: compaction-retry
// double-counting of ENTITY_COOCCUR weight; F2: silent, permanent data loss from WAL
// segment-number reuse after TruncateNode - see compact.go's package doc comment and
// task-3.1.3.md) were at exactly this seam, and neither was caught by a
// single-cycle, single-process test - both surfaced only once compaction ran a
// second time over a previously-compacted-and-truncated node. This test therefore
// runs THREE append-then-compact cycles against the same EdgeLog root and graph.dat
// path (reusing already-compacted nodes across cycles, exactly the regime F1/F2 were
// found in), plus a simulated process restart between cycles 2 and 3 (discarding the
// in-memory *CSRGraph and reloading graph.dat fresh via LoadCSR, then reopening a
// fresh *EdgeLog against the same root) to prove durability survives a restart, not
// just in-memory correctness within one process's lifetime.
//
// Correctness is checked against an independent, serially-computed oracle: a plain
// Go map built by iterating every appended edge in order and applying the same
// weight-aggregation/dedup rules compact.go's mergeEdges documents (ENTITY_COOCCUR
// sums Weight and takes max LastUpdated; every other type is deduplicated by
// (target, type) with the most-recently-updated occurrence winning outright), and an
// independent BFS mirroring GraphNeighbors' documented dedup/pruning/sort/cap
// semantics. The oracle is computed without calling mergeEdges or GraphNeighbors
// themselves, so a shared bug in both the production code and the oracle cannot
// silently cancel out.
package graph

import (
	"path/filepath"
	"reflect"
	"testing"
)

// oracleKey identifies one merged adjacency slot: a (source, target, type) triple.
type oracleKey struct {
	source uint64
	target uint64
	typ    EdgeType
}

// roundTripOracle is an independently-maintained model of what graph.dat's merged
// adjacency should contain after every edge appended so far, computed by serial
// iteration - never by calling mergeEdges or Compact.
type roundTripOracle struct {
	entries map[oracleKey]CSREdge
}

func newRoundTripOracle() *roundTripOracle {
	return &roundTripOracle{entries: make(map[oracleKey]CSREdge)}
}

// apply folds one freshly-appended edge (source -> edge.Target, edge.Type) into the
// oracle, mirroring compact.go's mergeEdges documented semantics exactly but
// implemented independently: ENTITY_COOCCUR sums Weight and takes the max
// LastUpdated across every occurrence; every other type is deduplicated by
// (source, target, type), with the occurrence having the greater-or-equal
// LastUpdated winning outright (ties favor the newer/later-applied entry, matching
// mergeEdges' "incoming edges are appended after existing entries" tie-break, since
// apply here is always called in append order too).
func (o *roundTripOracle) apply(source uint64, edge CSREdge) {
	k := oracleKey{source: source, target: edge.Target, typ: edge.Type}
	prev, ok := o.entries[k]
	if !ok {
		o.entries[k] = edge
		return
	}
	if edge.Type == EdgeEntityCooccur {
		sum := prev
		sum.Weight = prev.Weight + edge.Weight
		if edge.LastUpdated > prev.LastUpdated {
			sum.LastUpdated = edge.LastUpdated
		}
		o.entries[k] = sum
		return
	}
	if edge.LastUpdated >= prev.LastUpdated {
		o.entries[k] = edge
	}
}

// neighborsOf returns every oracle entry currently recorded for source, in no
// particular order (callers needing a stable order use oracleNeighbors' BFS, which
// applies GraphNeighbors' own documented sort).
func (o *roundTripOracle) neighborsOf(source uint64) []CSREdge {
	var out []CSREdge
	for k, e := range o.entries {
		if k.source == source {
			out = append(out, e)
		}
	}
	return out
}

// nodeCount and edgeCount mirror CSRGraph.NodeCount/EdgeCount for an invariant
// cross-check at each checkpoint: the number of distinct source fileIDs with at
// least one entry, and the total number of entries across all of them.
func (o *roundTripOracle) nodeCount() int {
	seen := make(map[uint64]bool)
	for k := range o.entries {
		seen[k.source] = true
	}
	return len(seen)
}

func (o *roundTripOracle) edgeCount() int { return len(o.entries) }

// oracleNeighbors independently reimplements GraphNeighbors' BFS traversal directly
// over the oracle's merged adjacency, mirroring its documented semantics: the
// edgeTypeFilter prunes traversal itself (a non-matching edge is never followed, not
// merely filtered from the final result), a node reached at multiple hop distances
// is recorded at the shortest one (first-seen-hop dedup), and the result is sorted
// (hop asc, Weight desc, Target asc) before the maxNodes cap is applied.
func oracleNeighbors(o *roundTripOracle, fileID uint64, depth int, filter EdgeType, maxNodes int) []CSREdge {
	if depth == 0 || maxNodes == 0 {
		return nil
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
			for _, e := range o.neighborsOf(node) {
				if filter != EdgeTypeInvalid && e.Type != filter {
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

	// Sort by (hop asc, Weight desc, Target asc), matching GraphNeighbors exactly.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0; j-- {
			a, b := candidates[j-1], candidates[j]
			less := func(a, b candidate) bool {
				if a.hop != b.hop {
					return a.hop < b.hop
				}
				if a.edge.Weight != b.edge.Weight {
					return a.edge.Weight > b.edge.Weight
				}
				return a.edge.Target < b.edge.Target
			}
			if less(b, a) {
				candidates[j-1], candidates[j] = candidates[j], candidates[j-1]
			} else {
				break
			}
		}
	}

	if len(candidates) > maxNodes {
		candidates = candidates[:maxNodes]
	}

	if len(candidates) == 0 {
		return nil
	}
	out := make([]CSREdge, len(candidates))
	for i, c := range candidates {
		out[i] = c.edge
	}
	return out
}

// mustEdge builds a validated CSREdge via edge.go's NewCSREdge (3.1.4), failing the
// test immediately on an unexpected construction error rather than silently
// appending a zero-value edge.
func mustEdge(t *testing.T, target uint64, typ EdgeType, weight uint32, lastUpdated int64) CSREdge {
	t.Helper()
	e, err := NewCSREdge(target, typ, weight, lastUpdated)
	if err != nil {
		t.Fatalf("NewCSREdge(%d, %v, %d, %d) unexpected error: %v", target, typ, weight, lastUpdated, err)
	}
	return e
}

// appendAndTrack appends edge to source's per-node log via log.AppendEdge and
// records it in oracle in the same call, so the test can never accidentally apply
// one without the other (a mismatch would itself indicate the harness, not the
// package under test, is broken).
func appendAndTrack(t *testing.T, log *EdgeLog, oracle *roundTripOracle, source uint64, edge CSREdge) {
	t.Helper()
	if err := log.AppendEdge(source, edge); err != nil {
		t.Fatalf("AppendEdge(source=%d, edge=%+v) unexpected error: %v", source, edge, err)
	}
	oracle.apply(source, edge)
}

// assertGraphNeighborsMatchesOracle calls GraphNeighbors against g and compares the
// result to oracleNeighbors' independently-computed prediction for the exact same
// query, failing with a detailed message on any mismatch.
func assertGraphNeighborsMatchesOracle(t *testing.T, label string, g *CSRGraph, oracle *roundTripOracle, fileID uint64, depth int, filter EdgeType, maxNodes int) []CSREdge {
	t.Helper()
	got, err := GraphNeighbors(g, fileID, depth, filter, maxNodes)
	if err != nil {
		t.Fatalf("%s: GraphNeighbors(fileID=%d, depth=%d, filter=%v, maxNodes=%d) unexpected error: %v", label, fileID, depth, filter, maxNodes, err)
	}
	want := oracleNeighbors(oracle, fileID, depth, filter, maxNodes)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: GraphNeighbors(fileID=%d, depth=%d, filter=%v, maxNodes=%d) mismatch:\n got  = %+v\n want = %+v", label, fileID, depth, filter, maxNodes, got, want)
	}
	return got
}

func TestGraphRoundTrip(t *testing.T) {
	dir := t.TempDir()
	graphPath := filepath.Join(dir, "graph.dat")
	logRoot := filepath.Join(dir, "edgelogs")

	// Six distinct source fileIDs, connected so that multi-hop traversal (depth 2)
	// and edge-type-filtered pruning both have something non-trivial to exercise:
	// 1 -> 2 (ENTITY_COOCCUR, appended twice to exercise weight summing)
	// 1 -> 3 (LLM_ASSERTED)
	// 2 -> 4 (SPLIT_SIBLING, appended twice with different LastUpdated to exercise
	//         last-write-wins dedup, not summing)
	// 3 -> 5 (REDIRECT)
	// 4 -> 6 (ENTITY_COOCCUR)
	oracle := newRoundTripOracle()

	log, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}

	// --- Cycle 1: initial inserts + first compaction ---
	appendAndTrack(t, log, oracle, 1, mustEdge(t, 2, EdgeEntityCooccur, 3, 100))
	appendAndTrack(t, log, oracle, 1, mustEdge(t, 2, EdgeEntityCooccur, 5, 110)) // same pair: weight sums to 8
	appendAndTrack(t, log, oracle, 1, mustEdge(t, 3, EdgeLLMAsserted, 1, 100))
	appendAndTrack(t, log, oracle, 2, mustEdge(t, 4, EdgeSplitSibling, 1, 100))
	appendAndTrack(t, log, oracle, 2, mustEdge(t, 4, EdgeSplitSibling, 1, 200)) // same triple, later LastUpdated wins (not summed)
	appendAndTrack(t, log, oracle, 3, mustEdge(t, 5, EdgeRedirect, 1, 100))
	appendAndTrack(t, log, oracle, 4, mustEdge(t, 6, EdgeEntityCooccur, 2, 100))

	g1, err := Compact(graphPath, log)
	if err != nil {
		t.Fatalf("cycle 1 Compact: %v", err)
	}

	if g1.NodeCount() != oracle.nodeCount() {
		t.Fatalf("cycle 1: NodeCount = %d, want %d (oracle)", g1.NodeCount(), oracle.nodeCount())
	}
	if g1.EdgeCount() != oracle.edgeCount() {
		t.Fatalf("cycle 1: EdgeCount = %d, want %d (oracle)", g1.EdgeCount(), oracle.edgeCount())
	}

	// Depth-0 boundary: always empty, no error, regardless of graph contents.
	if got, err := GraphNeighbors(g1, 1, 0, EdgeTypeInvalid, 10); err != nil || got != nil {
		t.Fatalf("cycle 1: GraphNeighbors depth=0 = (%v, %v), want (nil, nil)", got, err)
	}

	assertGraphNeighborsMatchesOracle(t, "cycle1/depth1/unfiltered", g1, oracle, 1, 1, EdgeTypeInvalid, 10)
	assertGraphNeighborsMatchesOracle(t, "cycle1/depth2/unfiltered", g1, oracle, 1, 2, EdgeTypeInvalid, 10)
	assertGraphNeighborsMatchesOracle(t, "cycle1/depth2/cooccurOnly", g1, oracle, 1, 2, EdgeEntityCooccur, 10)
	assertGraphNeighborsMatchesOracle(t, "cycle1/depth1/from3", g1, oracle, 3, 1, EdgeTypeInvalid, 10)

	// Sanity: the summed ENTITY_COOCCUR weight for 1->2 must be exactly 8 (3+5),
	// and the SPLIT_SIBLING 2->4 entry must carry LastUpdated=200 (the later
	// occurrence), not be duplicated into two entries.
	n1 := assertGraphNeighborsMatchesOracle(t, "cycle1/depth1/from1/cooccurOnly", g1, oracle, 1, 1, EdgeEntityCooccur, 10)
	if len(n1) != 1 || n1[0].Target != 2 || n1[0].Weight != 8 {
		t.Fatalf("cycle 1: expected single summed 1->2 ENTITY_COOCCUR edge with Weight=8, got %+v", n1)
	}
	n2 := assertGraphNeighborsMatchesOracle(t, "cycle1/depth1/from2", g1, oracle, 2, 1, EdgeTypeInvalid, 10)
	if len(n2) != 1 || n2[0].Target != 4 || n2[0].LastUpdated != 200 {
		t.Fatalf("cycle 1: expected single deduplicated 2->4 SPLIT_SIBLING edge with LastUpdated=200, got %+v", n2)
	}

	// --- Cycle 2: second round of inserts, including further edges on
	// already-compacted-and-truncated nodes (1, 2, 4) - the exact seam class F1/F2
	// were found at - plus a brand-new node/edge (6 -> 1, closing a cycle in the
	// graph to also exercise dedup-across-a-diamond at depth 2). ---
	appendAndTrack(t, log, oracle, 1, mustEdge(t, 2, EdgeEntityCooccur, 10, 300)) // further increment on an already-compacted pair: 8+10=18
	appendAndTrack(t, log, oracle, 1, mustEdge(t, 7, EdgeLLMAsserted, 1, 300))    // new target from an already-compacted node
	appendAndTrack(t, log, oracle, 4, mustEdge(t, 6, EdgeEntityCooccur, 4, 300))  // further increment: 2+4=6
	appendAndTrack(t, log, oracle, 6, mustEdge(t, 1, EdgeEntityCooccur, 1, 300))  // brand-new source node

	g2, err := Compact(graphPath, log)
	if err != nil {
		t.Fatalf("cycle 2 Compact: %v", err)
	}

	if g2.NodeCount() != oracle.nodeCount() {
		t.Fatalf("cycle 2: NodeCount = %d, want %d (oracle)", g2.NodeCount(), oracle.nodeCount())
	}
	if g2.EdgeCount() != oracle.edgeCount() {
		t.Fatalf("cycle 2: EdgeCount = %d, want %d (oracle)", g2.EdgeCount(), oracle.edgeCount())
	}

	preRestart1 := assertGraphNeighborsMatchesOracle(t, "cycle2/depth1/from1/cooccurOnly", g2, oracle, 1, 1, EdgeEntityCooccur, 10)
	if len(preRestart1) != 1 || preRestart1[0].Weight != 18 {
		t.Fatalf("cycle 2: expected 1->2 ENTITY_COOCCUR weight to have grown to 18 across two compaction cycles, got %+v", preRestart1)
	}
	preRestart2 := assertGraphNeighborsMatchesOracle(t, "cycle2/depth2/from1/unfiltered", g2, oracle, 1, 2, EdgeTypeInvalid, 20)

	// --- Simulated process restart: discard the in-memory *CSRGraph entirely and
	// reload graph.dat fresh from disk, exactly as a restarted process would - this
	// is the "full round trip implies durability across a restart" requirement, not
	// merely in-memory correctness within this test's own process lifetime. Also
	// close and reopen the EdgeLog against the same root, simulating the restarted
	// process reattaching to its own on-disk edge log state. ---
	if err := log.Close(); err != nil {
		t.Fatalf("closing edge log before simulated restart: %v", err)
	}
	g2 = nil // ensure nothing below can accidentally still reference the pre-restart in-memory graph

	reloaded, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("simulated restart: LoadCSR(%s): %v", graphPath, err)
	}
	if reloaded.NodeCount() != oracle.nodeCount() || reloaded.EdgeCount() != oracle.edgeCount() {
		t.Fatalf("simulated restart: reloaded NodeCount/EdgeCount = %d/%d, want %d/%d (oracle)", reloaded.NodeCount(), reloaded.EdgeCount(), oracle.nodeCount(), oracle.edgeCount())
	}

	postRestart1 := assertGraphNeighborsMatchesOracle(t, "post-restart/depth1/from1/cooccurOnly", reloaded, oracle, 1, 1, EdgeEntityCooccur, 10)
	if !reflect.DeepEqual(preRestart1, postRestart1) {
		t.Fatalf("post-restart result differs from pre-restart result: pre=%+v post=%+v", preRestart1, postRestart1)
	}
	postRestart2 := assertGraphNeighborsMatchesOracle(t, "post-restart/depth2/from1/unfiltered", reloaded, oracle, 1, 2, EdgeTypeInvalid, 20)
	if !reflect.DeepEqual(preRestart2, postRestart2) {
		t.Fatalf("post-restart depth-2 result differs from pre-restart result: pre=%+v post=%+v", preRestart2, postRestart2)
	}

	log2, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("simulated restart: reopening EdgeLog at %s: %v", logRoot, err)
	}
	defer log2.Close()

	// --- Cycle 3 (post-restart write path): append further edges via the
	// newly-reopened EdgeLog, including yet another increment on the
	// twice-already-compacted 1->2 ENTITY_COOCCUR pair (proving segment numbering
	// correctly resumed past whatever TruncateNode already floored it to, per
	// edgelog.go's WriteSegmentFloor fix - a silent regression here would either
	// error opening the writer or, worse, cause this increment to be silently lost
	// by the next Compact, exactly F2's failure mode), then compact and reload
	// again. ---
	appendAndTrack(t, log2, oracle, 1, mustEdge(t, 2, EdgeEntityCooccur, 2, 400)) // 18+2=20
	appendAndTrack(t, log2, oracle, 3, mustEdge(t, 5, EdgeRedirect, 1, 400))      // update existing 3->5, same LastUpdated tie broken to newer
	appendAndTrack(t, log2, oracle, 7, mustEdge(t, 1, EdgeLLMAsserted, 1, 400))   // new source node reachable from node 1 at hop 2 via 7? no: 1->7 exists, so 7 is a hop-1 target; 7->1 makes a cycle back

	g3, err := Compact(graphPath, log2)
	if err != nil {
		t.Fatalf("cycle 3 Compact: %v", err)
	}
	if g3.NodeCount() != oracle.nodeCount() || g3.EdgeCount() != oracle.edgeCount() {
		t.Fatalf("cycle 3: NodeCount/EdgeCount = %d/%d, want %d/%d (oracle)", g3.NodeCount(), g3.EdgeCount(), oracle.nodeCount(), oracle.edgeCount())
	}

	final, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("final reload: LoadCSR(%s): %v", graphPath, err)
	}

	finalN1 := assertGraphNeighborsMatchesOracle(t, "final/depth1/from1/cooccurOnly", final, oracle, 1, 1, EdgeEntityCooccur, 10)
	if len(finalN1) != 1 || finalN1[0].Weight != 20 {
		t.Fatalf("final: expected 1->2 ENTITY_COOCCUR weight to have grown to 20 across three compaction cycles spanning a simulated restart, got %+v", finalN1)
	}
	assertGraphNeighborsMatchesOracle(t, "final/depth2/from1/unfiltered", final, oracle, 1, 2, EdgeTypeInvalid, 20)
	assertGraphNeighborsMatchesOracle(t, "final/depth2/from1/llmAssertedOnly", final, oracle, 1, 2, EdgeLLMAsserted, 20)
	assertGraphNeighborsMatchesOracle(t, "final/depth1/from3", final, oracle, 3, 1, EdgeTypeInvalid, 20)
	assertGraphNeighborsMatchesOracle(t, "final/maxNodesCap", final, oracle, 1, 2, EdgeTypeInvalid, 1)
}
