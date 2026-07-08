// Package graph (this file): subtask 3.1.4's edge-type creation/validation support.
//
// Every earlier subtask in this package (3.1.1's csr.go, 3.1.2's edgelog.go, 3.1.3's
// compact.go and edge_append.go) explicitly deferred full validation of the EdgeType
// enum to this subtask: edgelog.go's AppendEdge rejected only the EdgeTypeInvalid
// zero-value sentinel, csr.go's decodeCSREdge performed no type validation at all on
// decode, and edge_append.go added the EdgeEntityCooccur/EdgeLLMAsserted constants
// ahead of this subtask purely because 3.1.3's own compaction test spec needed to
// exercise ENTITY_COOCCUR edges. This file is what makes those four EdgeType values -
// SPLIT_SIBLING, REDIRECT, ENTITY_COOCCUR, LLM_ASSERTED (see docs/LLD/graph.md's "Edge
// shape" section for the canonical names) - a fully validated, canonically-named set,
// and provides a validated CSREdge constructor for callers that create edges.
//
// This file does NOT change edge_append.go's EdgeAppender, which docs/LLD/graph.md
// explicitly scopes to SPLIT_SIBLING/REDIRECT only (it is a distinct, narrower
// mechanism from EdgeLog/CSR, which carry all four types) - EdgeAppender's existing
// decodeEdge/AppendEdge type checks already correctly enforce that narrower scope and
// are left unchanged.
package graph

import "fmt"

// ValidEdgeType reports whether t is one of the edge types this package's CSR/edge-log
// stack recognizes: EdgeSplitSibling, EdgeRedirect, EdgeEntityCooccur, or
// EdgeLLMAsserted. EdgeTypeInvalid (the zero value) and any other undefined byte value
// are not valid.
func ValidEdgeType(t EdgeType) bool {
	switch t {
	case EdgeSplitSibling, EdgeRedirect, EdgeEntityCooccur, EdgeLLMAsserted:
		return true
	default:
		return false
	}
}

// EdgeTypeName returns t's canonical on-the-wire/API name, exactly matching the tokens
// docs/LLD/graph.md's "Edge shape" section names (ENTITY_COOCCUR, LLM_ASSERTED,
// SPLIT_SIBLING, REDIRECT). It returns an error for any type ValidEdgeType rejects, and
// is distinct from EdgeType.String() (edge_append.go), which is a debug-format helper
// used in error messages that never errors and does not use these canonical tokens.
func EdgeTypeName(t EdgeType) (string, error) {
	switch t {
	case EdgeEntityCooccur:
		return "ENTITY_COOCCUR", nil
	case EdgeLLMAsserted:
		return "LLM_ASSERTED", nil
	case EdgeSplitSibling:
		return "SPLIT_SIBLING", nil
	case EdgeRedirect:
		return "REDIRECT", nil
	default:
		return "", fmt.Errorf("graph: invalid edge type %v has no canonical name", t)
	}
}

// ParseEdgeType parses name (an exact, case-sensitive match against one of
// ENTITY_COOCCUR, LLM_ASSERTED, SPLIT_SIBLING, REDIRECT) into the corresponding
// EdgeType. It is the inverse of EdgeTypeName, provided ahead of subtask 3.1.5's
// GraphNeighbors edgeTypeFilter parameter (docs/LLD/graph.md's "Traversal API"
// section), which will need to parse a caller-supplied type filter into an EdgeType.
func ParseEdgeType(name string) (EdgeType, error) {
	switch name {
	case "ENTITY_COOCCUR":
		return EdgeEntityCooccur, nil
	case "LLM_ASSERTED":
		return EdgeLLMAsserted, nil
	case "SPLIT_SIBLING":
		return EdgeSplitSibling, nil
	case "REDIRECT":
		return EdgeRedirect, nil
	default:
		return EdgeTypeInvalid, fmt.Errorf("graph: unrecognized edge type name %q", name)
	}
}

// NewCSREdge constructs a validated CSREdge, rejecting any target/type combination
// whose Type is not one of the four types ValidEdgeType recognizes. This is the
// canonical, validated way for a caller (e.g. the ingestion segmentation agent, or a
// test) to build a CSREdge for use with EdgeLog.AppendEdge or BuildCSR/WriteCSR, rather
// than constructing the struct literal directly and risking an undefined type byte
// reaching those lower-level, best-effort-durable layers.
func NewCSREdge(target uint64, t EdgeType, weight uint32, lastUpdated int64) (CSREdge, error) {
	if !ValidEdgeType(t) {
		return CSREdge{}, fmt.Errorf("graph: cannot create edge with invalid type %v", t)
	}
	return CSREdge{
		Target:      target,
		Type:        t,
		Weight:      weight,
		LastUpdated: lastUpdated,
	}, nil
}
