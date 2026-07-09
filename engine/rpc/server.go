// Package rpc (this file): task-3.2.2's HiveMind gRPC server implementation (GitHub issue
// #16, Epic Phase 3). Server is a thin adapter over the already-implemented storage engine
// packages (engine/catalog, engine/graph, engine/btree): every handler unmarshals its
// request, calls the real underlying engine function, marshals the response, and maps
// internal errors to gRPC status codes. No new business logic lives here.
//
// Scope: this file implements exactly the 5 RPCs docs/LLD/rpc.md's "Exposed RPCs" section
// names -- PutSegment, GetFile, ReadPartial, GraphNeighbors, SearchCandidates -- the RPCs
// engine/rpc/'s server SERVES. ProposeSplit is a client-side call this engine MAKES against
// the Python agent service (engine/split/proposer_grpc.go, task-3.2.3); Server does not
// implement it here and instead falls back to the generated
// hivemindv1.UnimplementedHiveMindServer's default (codes.Unimplemented) via embedding. See
// requirement.md/impact-analysis.json under
// .cdr/runs/2026-07-09/002-implementation/ for the full scope-boundary justification.
package rpc

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/graph"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
)

// Server implements hivemindv1.HiveMindServer as a thin adapter over the already-built
// storage engine packages. It owns none of its dependencies' lifecycles (does not open or
// close cat/cs/idAlloc/g/btreeStore) -- callers construct and close those, then pass them to
// NewServer, mirroring catalog.OpenContentStore's "does not own cat/w lifecycle" convention.
type Server struct {
	hivemindv1.UnimplementedHiveMindServer

	cat     *catalog.Catalog
	cs      *catalog.ContentStore
	idAlloc *catalog.IDAllocator

	// g backs GraphNeighbors. A nil g is valid (graph.GraphNeighbors treats g == nil as an
	// empty graph, returning no neighbors rather than erroring) -- lets Server be
	// constructed before any graph.dat snapshot has been built/loaded.
	g *graph.CSRGraph

	// btreeStore/btreeRootNodeID back SearchCandidates. Nothing in this file's 5 in-scope
	// RPCs writes to the B+Tree: PutSegmentRequest (proto/hivemind.proto) carries only
	// file_id + content, no path, so there is no path for PutSegment to index. The B+Tree
	// is therefore purely an injected read dependency here, populated by whatever caller
	// outside this RPC surface owns path->fileID indexing. A nil btreeStore is valid
	// (SearchCandidates returns an empty result set rather than erroring).
	btreeStore      *btree.NodeStore
	btreeRootNodeID uint64
}

// NewServer constructs a Server backed by the given already-open engine dependencies. cat,
// cs, and idAlloc must be non-nil (every in-scope RPC except GraphNeighbors/SearchCandidates
// needs them); g and btreeStore may be nil (see their field docs above).
func NewServer(cat *catalog.Catalog, cs *catalog.ContentStore, idAlloc *catalog.IDAllocator, g *graph.CSRGraph, btreeStore *btree.NodeStore, btreeRootNodeID uint64) (*Server, error) {
	if cat == nil {
		return nil, fmt.Errorf("rpc: NewServer: cat must not be nil")
	}
	if cs == nil {
		return nil, fmt.Errorf("rpc: NewServer: cs must not be nil")
	}
	if idAlloc == nil {
		return nil, fmt.Errorf("rpc: NewServer: idAlloc must not be nil")
	}
	return &Server{
		cat:             cat,
		cs:              cs,
		idAlloc:         idAlloc,
		g:               g,
		btreeStore:      btreeStore,
		btreeRootNodeID: btreeRootNodeID,
	}, nil
}

// mapCatalogError maps a catalog/content-package error to a gRPC status error: wrapped
// catalog.ErrNotFound becomes codes.NotFound (the fileID genuinely doesn't exist -- a
// well-formed request, not a caller mistake in shape), everything else becomes
// codes.Internal (an unexpected internal fault: disk I/O, WAL, encoding, etc.). op names the
// RPC method for the status message.
func mapCatalogError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, catalog.ErrNotFound) {
		return status.Errorf(codes.NotFound, "rpc: %s: %v", op, err)
	}
	return status.Errorf(codes.Internal, "rpc: %s: %v", op, err)
}

// PutSegment writes a segment produced by the ingestion segmentation agent into a topic
// file. Mirrors engine/catalog.ContentStore's Create/Append split exactly, per
// PutSegmentRequest.file_id's documented semantics (proto/hivemind.proto): file_id == 0
// means create a new file (a fresh fileID is allocated via the injected IDAllocator);
// file_id != 0 means append to the existing file.
//
// See the Server doc comment for why PutSegment does not (and, given
// PutSegmentRequest's current proto shape with no path field, cannot) perform any B+Tree
// path-index insert.
func (s *Server) PutSegment(ctx context.Context, req *hivemindv1.PutSegmentRequest) (*hivemindv1.PutSegmentResponse, error) {
	if req.GetFileId() == catalog.InvalidFileID {
		fileID, err := s.idAlloc.Next()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "rpc: PutSegment: allocating fileID: %v", err)
		}

		rec := catalog.CatalogRecord{
			FileID:         fileID,
			CurrentVersion: 1,
			SizeBytes:      uint64(len(req.GetContent())),
			Status:         catalog.StatusActive,
		}
		if _, err := s.cs.Create(rec, req.GetContent()); err != nil {
			return nil, mapCatalogError("PutSegment (create)", err)
		}

		return &hivemindv1.PutSegmentResponse{
			FileId:     fileID,
			NewVersion: rec.CurrentVersion,
		}, nil
	}

	fileID := req.GetFileId()
	if _, err := s.cs.Append(fileID, req.GetContent()); err != nil {
		return nil, mapCatalogError("PutSegment (append)", err)
	}

	rec, err := s.cat.Get(fileID)
	if err != nil {
		return nil, mapCatalogError("PutSegment (append, post-write version lookup)", err)
	}

	return &hivemindv1.PutSegmentResponse{
		FileId:     fileID,
		NewVersion: rec.CurrentVersion,
	}, nil
}

// GetFile performs a full-file read at the current (pre-MVCC, single-version) snapshot.
// Delegates to engine/catalog.ContentStore.Read for content and engine/catalog.Catalog.Get
// for the file's CurrentVersion.
func (s *Server) GetFile(ctx context.Context, req *hivemindv1.GetFileRequest) (*hivemindv1.GetFileResponse, error) {
	fileID := req.GetFileId()
	if fileID == catalog.InvalidFileID {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: GetFile: file_id %d is invalid (proto3 zero-value / unset field)", fileID)
	}

	content, err := s.cs.Read(fileID)
	if err != nil {
		return nil, mapCatalogError("GetFile", err)
	}

	rec, err := s.cat.Get(fileID)
	if err != nil {
		return nil, mapCatalogError("GetFile (version lookup)", err)
	}

	return &hivemindv1.GetFileResponse{
		Content: content,
		Version: rec.CurrentVersion,
	}, nil
}

// ReadPartial performs a section-level read using the markdown header-offset cache.
// Delegates directly to engine/catalog.ContentStore.ReadPartial.
func (s *Server) ReadPartial(ctx context.Context, req *hivemindv1.ReadPartialRequest) (*hivemindv1.ReadPartialResponse, error) {
	fileID := req.GetFileId()
	if fileID == catalog.InvalidFileID {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: ReadPartial: file_id %d is invalid (proto3 zero-value / unset field)", fileID)
	}

	headers, err := s.cs.ReadPartial(fileID)
	if err != nil {
		return nil, mapCatalogError("ReadPartial", err)
	}

	out := make([]*hivemindv1.HeaderOffset, len(headers))
	for i, h := range headers {
		out[i] = &hivemindv1.HeaderOffset{
			Header: h.Header,
			Offset: int64(h.Offset),
		}
	}

	return &hivemindv1.ReadPartialResponse{Headers: out}, nil
}

// protoEdgeTypeToGraph converts a wire-level hivemindv1.EdgeType to engine/graph's internal
// EdgeType by NAME, not by numeric cast. The two enums' numeric values do not line up:
// graph.EdgeType's iota order is EdgeTypeInvalid=0, EdgeSplitSibling=1, EdgeRedirect=2,
// EdgeEntityCooccur=3, EdgeLLMAsserted=4 (engine/graph/edge_append.go), while
// hivemindv1.EdgeType's proto-declared order is EDGE_TYPE_UNSPECIFIED=0, ENTITY_COOCCUR=1,
// LLM_ASSERTED=2, SPLIT_SIBLING=3, REDIRECT=4 (proto/hivemind.proto). A numeric cast would
// silently produce the wrong filter/result edge types.
func protoEdgeTypeToGraph(t hivemindv1.EdgeType) (graph.EdgeType, error) {
	switch t {
	case hivemindv1.EdgeType_EDGE_TYPE_UNSPECIFIED:
		return graph.EdgeTypeInvalid, nil
	case hivemindv1.EdgeType_ENTITY_COOCCUR:
		return graph.EdgeEntityCooccur, nil
	case hivemindv1.EdgeType_LLM_ASSERTED:
		return graph.EdgeLLMAsserted, nil
	case hivemindv1.EdgeType_SPLIT_SIBLING:
		return graph.EdgeSplitSibling, nil
	case hivemindv1.EdgeType_REDIRECT:
		return graph.EdgeRedirect, nil
	default:
		return graph.EdgeTypeInvalid, fmt.Errorf("rpc: unrecognized EdgeType %v", t)
	}
}

// graphEdgeTypeToProto is protoEdgeTypeToGraph's inverse, same name-based mapping rationale.
func graphEdgeTypeToProto(t graph.EdgeType) (hivemindv1.EdgeType, error) {
	switch t {
	case graph.EdgeEntityCooccur:
		return hivemindv1.EdgeType_ENTITY_COOCCUR, nil
	case graph.EdgeLLMAsserted:
		return hivemindv1.EdgeType_LLM_ASSERTED, nil
	case graph.EdgeSplitSibling:
		return hivemindv1.EdgeType_SPLIT_SIBLING, nil
	case graph.EdgeRedirect:
		return hivemindv1.EdgeType_REDIRECT, nil
	default:
		return hivemindv1.EdgeType_EDGE_TYPE_UNSPECIFIED, fmt.Errorf("rpc: unrecognized graph.EdgeType %v", t)
	}
}

// GraphNeighbors performs 0-2 hop graph traversal, delegating to engine/graph.GraphNeighbors.
//
// Known limitation (see .cdr/runs/2026-07-09/002-implementation/impact-analysis.json):
// engine/graph.GraphNeighbors's return type ([]graph.CSREdge) does not expose the hop
// distance at which each neighbor was reached, even though its own internal BFS computes
// one -- hop is not part of CSREdge. Reimplementing hop-tracking here would duplicate that
// package's traversal logic (forbidden: this file is a thin adapter, no new business
// logic). Every Neighbor.hop in this handler's response is therefore left at its proto zero
// value (0), not a faithful hop distance. This is a genuine engine/graph API gap, not an
// oversight local to this handler; extending graph.GraphNeighbors (or adding a
// hop-preserving variant) is a candidate follow-up outside 3.2.2's scope.
func (s *Server) GraphNeighbors(ctx context.Context, req *hivemindv1.GraphNeighborsRequest) (*hivemindv1.GraphNeighborsResponse, error) {
	depth := int(req.GetDepth())
	maxNodes := int(req.GetMaxNodes())

	if depth < 0 || depth > 2 {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: GraphNeighbors: depth %d out of range [0, 2]", depth)
	}
	if maxNodes < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: GraphNeighbors: max_nodes %d must be >= 0", maxNodes)
	}

	edgeTypeFilter, err := protoEdgeTypeToGraph(req.GetEdgeTypeFilter())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: GraphNeighbors: %v", err)
	}

	edges, err := graph.GraphNeighbors(s.g, req.GetFileId(), depth, edgeTypeFilter, maxNodes)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: GraphNeighbors: %v", err)
	}

	neighbors := make([]*hivemindv1.Neighbor, len(edges))
	for i, e := range edges {
		protoType, err := graphEdgeTypeToProto(e.Type)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "rpc: GraphNeighbors: %v", err)
		}
		neighbors[i] = &hivemindv1.Neighbor{
			TargetFileId: e.Target,
			Type:         protoType,
			Weight:       e.Weight,
			// Hop intentionally left at 0 -- see doc comment above.
		}
	}

	return &hivemindv1.GraphNeighborsResponse{Neighbors: neighbors}, nil
}

// searchCandidateScore is a constant placeholder relevance score assigned to every
// SearchCandidates result. engine/btree exposes no relevance-scoring primitive (PrefixScan
// returns unranked (path, fileID) pairs in sorted-path order only) and no relevance-ranking
// algorithm exists anywhere else in this codebase yet -- inventing one here would be new
// business logic outside this subtask's thin-adapter scope. See
// .cdr/runs/2026-07-09/002-implementation/impact-analysis.json.
const searchCandidateScore float32 = 1.0

// SearchCandidates performs a non-LLM candidate topic search, delegating to
// engine/btree.PrefixScan. The issue's acceptance criteria names "btree" as
// SearchCandidates' delegation target; PrefixScan is the only query-shaped read primitive
// btree exposes (Lookup is exact-match only), so SearchCandidatesRequest.query is treated as
// a literal string prefix, not a general/fuzzy query. See searchCandidateScore's doc comment
// for why CandidateTopic.score is a constant placeholder rather than a computed value.
//
// max_results semantics: unlike GraphNeighborsRequest.max_nodes (whose proto doc comment
// explicitly defines 0 as "empty result"), SearchCandidatesRequest.max_results has no such
// documented zero-value semantic in proto/hivemind.proto. This handler treats 0 as "no cap"
// (return every PrefixScan match) rather than "return nothing", the more useful default for
// a search-style RPC; a positive value caps the result count. This interpretation choice is
// called out here explicitly since it is not dictated by the proto contract itself.
func (s *Server) SearchCandidates(ctx context.Context, req *hivemindv1.SearchCandidatesRequest) (*hivemindv1.SearchCandidatesResponse, error) {
	if s.btreeStore == nil {
		return &hivemindv1.SearchCandidatesResponse{}, nil
	}

	maxResults := int(req.GetMaxResults())
	if maxResults < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: SearchCandidates: max_results %d must be >= 0", maxResults)
	}

	entries, err := btree.PrefixScan(s.btreeStore, s.btreeRootNodeID, req.GetQuery())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rpc: SearchCandidates: %v", err)
	}

	if maxResults > 0 && len(entries) > maxResults {
		entries = entries[:maxResults]
	}

	candidates := make([]*hivemindv1.CandidateTopic, len(entries))
	for i, e := range entries {
		candidates[i] = &hivemindv1.CandidateTopic{
			FileId: e.FileID,
			Path:   e.Path,
			Score:  searchCandidateScore,
		}
	}

	return &hivemindv1.SearchCandidatesResponse{Candidates: candidates}, nil
}
