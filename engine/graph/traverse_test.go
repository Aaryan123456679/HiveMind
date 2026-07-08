package graph

import (
	"testing"
)

func mustCSREdge(t *testing.T, target uint64, typ EdgeType, weight uint32, lastUpdated int64) CSREdge {
	t.Helper()
	e, err := NewCSREdge(target, typ, weight, lastUpdated)
	if err != nil {
		t.Fatalf("NewCSREdge(%d, %v, %d, %d) failed: %v", target, typ, weight, lastUpdated, err)
	}
	return e
}

func targets(edges []CSREdge) map[uint64]bool {
	out := make(map[uint64]bool, len(edges))
	for _, e := range edges {
		out[e.Target] = true
	}
	return out
}

func TestGraphNeighbors(t *testing.T) {
	t.Run("Basic1Hop", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{
			1: {
				mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100),
				mustCSREdge(t, 3, EdgeLLMAsserted, 1, 100),
			},
		})
		got, err := GraphNeighbors(g, 1, 1, EdgeTypeInvalid, 10)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		want := map[uint64]bool{2: true, 3: true}
		if gotT := targets(got); len(gotT) != len(want) || !mapsEqual(gotT, want) {
			t.Fatalf("got targets %v, want %v", gotT, want)
		}
	})

	t.Run("TwoHopExpansion", func(t *testing.T) {
		// chain: 1 -> 2 -> 3 -> 4
		g := BuildCSR(map[uint64][]CSREdge{
			1: {mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100)},
			2: {mustCSREdge(t, 3, EdgeEntityCooccur, 1, 100)},
			3: {mustCSREdge(t, 4, EdgeEntityCooccur, 1, 100)},
		})
		got, err := GraphNeighbors(g, 1, 2, EdgeTypeInvalid, 10)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		gotT := targets(got)
		want := map[uint64]bool{2: true, 3: true}
		if !mapsEqual(gotT, want) {
			t.Fatalf("got targets %v, want %v (4 must NOT be reachable within 2 hops)", gotT, want)
		}
	})

	t.Run("CapExactlyN", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{
			1: {
				mustCSREdge(t, 2, EdgeEntityCooccur, 5, 100),
				mustCSREdge(t, 3, EdgeEntityCooccur, 4, 100),
				mustCSREdge(t, 4, EdgeEntityCooccur, 3, 100),
			},
		})
		got, err := GraphNeighbors(g, 1, 1, EdgeTypeInvalid, 3)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d results, want 3 (exact reachable count, no truncation)", len(got))
		}
	})

	t.Run("CapNPlus1", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{
			1: {
				mustCSREdge(t, 2, EdgeEntityCooccur, 5, 100),
				mustCSREdge(t, 3, EdgeEntityCooccur, 4, 100),
				mustCSREdge(t, 4, EdgeEntityCooccur, 3, 100),
			},
		})
		got, err := GraphNeighbors(g, 1, 1, EdgeTypeInvalid, 2)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d results, want 2 (capped)", len(got))
		}
		// Ordering: hop equal for all, so Weight descending -> target 2 (w=5), target 3 (w=4)
		// survive; target 4 (w=3, lowest) is dropped.
		gotT := targets(got)
		want := map[uint64]bool{2: true, 3: true}
		if !mapsEqual(gotT, want) {
			t.Fatalf("got targets %v, want %v (lowest-weight survivor dropped)", gotT, want)
		}
	})

	t.Run("CapZero", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{
			1: {mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100)},
		})
		got, err := GraphNeighbors(g, 1, 1, EdgeTypeInvalid, 0)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d results, want 0 (maxNodes=0)", len(got))
		}
	})

	t.Run("DepthZero", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{
			1: {mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100)},
		})
		got, err := GraphNeighbors(g, 1, 0, EdgeTypeInvalid, 10)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d results, want 0 (depth=0)", len(got))
		}
	})

	t.Run("TypeFilterNoMatches", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{
			1: {mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100)},
		})
		got, err := GraphNeighbors(g, 1, 1, EdgeSplitSibling, 10)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d results, want 0 (filter matches nothing)", len(got))
		}
	})

	t.Run("TypeFilterAllTypesRequested", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{
			1: {
				mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100),
				mustCSREdge(t, 3, EdgeLLMAsserted, 1, 100),
				mustCSREdge(t, 4, EdgeSplitSibling, 1, 100),
				mustCSREdge(t, 5, EdgeRedirect, 1, 100),
			},
		})
		cases := []struct {
			filter EdgeType
			want   map[uint64]bool
		}{
			{EdgeEntityCooccur, map[uint64]bool{2: true}},
			{EdgeLLMAsserted, map[uint64]bool{3: true}},
			{EdgeSplitSibling, map[uint64]bool{4: true}},
			{EdgeRedirect, map[uint64]bool{5: true}},
			{EdgeTypeInvalid, map[uint64]bool{2: true, 3: true, 4: true, 5: true}},
		}
		for _, c := range cases {
			got, err := GraphNeighbors(g, 1, 1, c.filter, 10)
			if err != nil {
				t.Fatalf("GraphNeighbors(filter=%v): %v", c.filter, err)
			}
			if gotT := targets(got); !mapsEqual(gotT, c.want) {
				t.Fatalf("filter=%v: got targets %v, want %v", c.filter, gotT, c.want)
			}
		}
	})

	t.Run("TypeFilterSingleType", func(t *testing.T) {
		// mixed types across 2 hops; filter to ENTITY_COOCCUR only, present at both hops.
		g := BuildCSR(map[uint64][]CSREdge{
			1: {
				mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100),
				mustCSREdge(t, 3, EdgeLLMAsserted, 1, 100),
			},
			2: {mustCSREdge(t, 4, EdgeEntityCooccur, 1, 100)},
			3: {mustCSREdge(t, 5, EdgeEntityCooccur, 1, 100)},
		})
		got, err := GraphNeighbors(g, 1, 2, EdgeEntityCooccur, 10)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		// node 3 is only reachable via the LLM_ASSERTED edge, which is filtered out, so 5
		// (only reachable via 3) must also be absent.
		want := map[uint64]bool{2: true, 4: true}
		if gotT := targets(got); !mapsEqual(gotT, want) {
			t.Fatalf("got targets %v, want %v", gotT, want)
		}
	})

	t.Run("InvalidDepthRejected", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{1: {mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100)}})
		for _, d := range []int{-1, 3} {
			if _, err := GraphNeighbors(g, 1, d, EdgeTypeInvalid, 10); err == nil {
				t.Fatalf("depth=%d: expected error, got nil", d)
			}
		}
	})

	t.Run("InvalidEdgeTypeFilterRejected", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{1: {mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100)}})
		if _, err := GraphNeighbors(g, 1, 1, EdgeType(200), 10); err == nil {
			t.Fatal("expected error for undefined edgeTypeFilter, got nil")
		}
	})

	t.Run("NegativeMaxNodesRejected", func(t *testing.T) {
		g := BuildCSR(map[uint64][]CSREdge{1: {mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100)}})
		if _, err := GraphNeighbors(g, 1, 1, EdgeTypeInvalid, -1); err == nil {
			t.Fatal("expected error for negative maxNodes, got nil")
		}
	})

	t.Run("DedupNoDoubleCounting", func(t *testing.T) {
		// diamond: 1 -> 2, 1 -> 3, 2 -> 4, 3 -> 4
		g := BuildCSR(map[uint64][]CSREdge{
			1: {
				mustCSREdge(t, 2, EdgeEntityCooccur, 1, 100),
				mustCSREdge(t, 3, EdgeEntityCooccur, 1, 100),
			},
			2: {mustCSREdge(t, 4, EdgeEntityCooccur, 1, 100)},
			3: {mustCSREdge(t, 4, EdgeEntityCooccur, 1, 100)},
		})
		got, err := GraphNeighbors(g, 1, 2, EdgeTypeInvalid, 10)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		count4 := 0
		for _, e := range got {
			if e.Target == 4 {
				count4++
			}
		}
		if count4 != 1 {
			t.Fatalf("node 4 appeared %d times in result, want exactly 1 (no double-counting)", count4)
		}
		if len(got) != 3 {
			t.Fatalf("got %d results, want 3 (2, 3, 4 each once)", len(got))
		}
	})

	t.Run("NilGraph", func(t *testing.T) {
		got, err := GraphNeighbors(nil, 1, 2, EdgeTypeInvalid, 10)
		if err != nil {
			t.Fatalf("GraphNeighbors(nil graph): %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d results, want 0 (nil graph)", len(got))
		}
	})

	t.Run("LargeGraphExceedsCapWithin2Hops", func(t *testing.T) {
		// Matches the issue's literal test spec: build a graph with more than maxNodes
		// reachable nodes within 2 hops, assert the traversal result size is capped at
		// maxNodes and respects a type filter. Node 1 has 20 direct (hop-1)
		// EdgeEntityCooccur neighbors (2..21) AND 20 direct (hop-1) EdgeLLMAsserted
		// neighbors (102..121); each EdgeEntityCooccur target also has its own unique
		// hop-2 EdgeEntityCooccur neighbor, for 60 reachable nodes total within 2 hops -
		// comfortably more than maxNodes. Note: the type filter constrains which edges
		// are traversed through, not just which are returned - a node only reachable via
		// a non-matching intermediate edge is unreachable under that filter (see
		// traverse.go's doc comment). Hop-2 targets are therefore reached only through
		// matching-type hop-1 edges in this fixture.
		adjacency := map[uint64][]CSREdge{}
		var hop1 []CSREdge
		for i := uint64(2); i <= 21; i++ {
			hop1 = append(hop1, mustCSREdge(t, i, EdgeEntityCooccur, uint32(i), 100))
			adjacency[i] = []CSREdge{mustCSREdge(t, i+1000, EdgeEntityCooccur, 1, 100)}
		}
		for i := uint64(102); i <= 121; i++ {
			hop1 = append(hop1, mustCSREdge(t, i, EdgeLLMAsserted, 1, 100))
		}
		adjacency[1] = hop1
		g := BuildCSR(adjacency)

		const maxNodes = 5
		got, err := GraphNeighbors(g, 1, 2, EdgeTypeInvalid, maxNodes)
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		if len(got) != maxNodes {
			t.Fatalf("got %d results, want exactly maxNodes=%d (60 nodes reachable within 2 hops)", len(got), maxNodes)
		}

		// Type filter respected even under the cap: filtering to EdgeLLMAsserted (only
		// present at hop 1, 20 candidates) must return only LLM_ASSERTED edges, still
		// capped at maxNodes.
		gotFiltered, err := GraphNeighbors(g, 1, 2, EdgeLLMAsserted, maxNodes)
		if err != nil {
			t.Fatalf("GraphNeighbors (filtered): %v", err)
		}
		if len(gotFiltered) != maxNodes {
			t.Fatalf("got %d filtered results, want exactly maxNodes=%d", len(gotFiltered), maxNodes)
		}
		for _, e := range gotFiltered {
			if e.Type != EdgeLLMAsserted {
				t.Fatalf("filtered result contains non-LLM_ASSERTED edge: %+v", e)
			}
			if e.Target < 102 || e.Target > 121 {
				t.Fatalf("filtered result contains unexpected target %d, want one of 102..121", e.Target)
			}
		}
	})
}

func mapsEqual(a, b map[uint64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
