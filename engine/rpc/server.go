// Package rpc (this file): task-3.2.2's HiveMind gRPC server implementation (GitHub issue
// #16, Epic Phase 3). Server is a thin adapter over the already-implemented storage engine
// packages (engine/catalog, engine/graph, engine/btree): every handler unmarshals its
// request, calls the real underlying engine function, marshals the response, and maps
// internal errors to gRPC status codes. No new business logic lives here.
//
// Scope: this file originally implemented exactly the 5 RPCs docs/LLD/rpc.md's "Exposed
// RPCs" section named at the time -- PutSegment, GetFile, ReadPartial, GraphNeighbors,
// SearchCandidates -- the RPCs engine/rpc/'s server SERVES. ProposeSplit is a client-side
// call this engine MAKES against the Python agent service
// (engine/split/proposer_grpc.go, task-3.2.3); Server does not implement it here and
// instead falls back to the generated hivemindv1.UnimplementedHiveMindServer's default
// (codes.Unimplemented) via embedding. See requirement.md/impact-analysis.json under
// .cdr/runs/2026-07-09/002-implementation/ for the full scope-boundary justification.
//
// PutEdge/PutEntity/LookupEntity (bottom of this file) were added later, as
// user-authorized new scope discovered during issue #18 subtask 3.4.4's verification (see
// .cdr/runs/2026-07-10/011-implementation/requirement.md) -- not a renumbered 3.2.x
// subtask. Same thin-adapter discipline applies: no new graph/btree business logic lives
// here, only request/response marshaling over engine/graph.EdgeLog and a dedicated
// engine/btree.Tree.
package rpc

import (
	"context"
	"errors"
	"fmt"
	"time"

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

	// edgeLog backs PutEdge (new scope, see this file's package doc comment above). A nil
	// edgeLog is valid -- PutEdge returns codes.Unavailable rather than panicking or
	// silently dropping the edge write, since (unlike SearchCandidates' empty-result
	// degraded mode) silently discarding a caller's edge write would be a worse failure
	// mode than a clear, immediate error.
	edgeLog *graph.EdgeLog

	// entityIndex backs PutEntity/LookupEntity (new scope, see this file's package doc
	// comment above). Deliberately a SEPARATE *btree.Tree from btreeStore/btreeRootNodeID
	// above (which stay read-only, exactly as before this change): entity-index writes
	// need a concurrency-safe, self-tracking root (btree.Tree.Insert/Root), and keeping it
	// in a wholly separate tree means entity keys can never leak into SearchCandidates'
	// path-prefix scans, regardless of key-namespacing correctness. A nil entityIndex is
	// valid -- PutEntity/LookupEntity return codes.Unavailable rather than panicking.
	entityIndex *btree.Tree

	// now is the entity-index/edge-timestamp clock, overridable by tests (see
	// server_test.go) so weight/LastUpdated-ordering assertions don't depend on wall-clock
	// timing. Defaults to time.Now in NewServer.
	now func() time.Time
}

// NewServer constructs a Server backed by the given already-open engine dependencies. cat,
// cs, and idAlloc must be non-nil (every in-scope RPC except GraphNeighbors/SearchCandidates
// needs them); g, btreeStore, edgeLog, and entityIndex may all be nil (see their field docs
// above).
func NewServer(cat *catalog.Catalog, cs *catalog.ContentStore, idAlloc *catalog.IDAllocator, g *graph.CSRGraph, btreeStore *btree.NodeStore, btreeRootNodeID uint64, edgeLog *graph.EdgeLog, entityIndex *btree.Tree) (*Server, error) {
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
		edgeLog:         edgeLog,
		entityIndex:     entityIndex,
		now:             time.Now,
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

// entityIndexPrefix namespaces every entity.idx key under a leading NUL byte, which sorts
// before every ordinary printable path SearchCandidates' own tree stores -- belt-and-
// suspenders isolation, since entityIndex is already a wholly separate *btree.Tree from
// btreeStore/btreeRootNodeID (see Server's field docs above), not something either
// PutEntity/LookupEntity or SearchCandidates actually depends on for correctness today.
const entityIndexPrefix = "\x00entity\x00"

// entityKeyPrefix returns the common key prefix every (entityName, *) association is
// stored under, used both by entityKey (below) and directly by LookupEntity's PrefixScan.
func entityKeyPrefix(entityName string) string {
	return entityIndexPrefix + entityName + "\x00"
}

// entityKey returns the unique B+Tree leaf key for one (entityName, fileID) association.
// engine/btree.Insert upserts a single fileID per key (one entity name alone cannot map to
// multiple files under that primitive) -- entityKey works around this by giving every
// distinct fileID registered against the same entityName its own key, suffixed with
// fileID itself, zero-padded to 20 base-10 digits (uint64's max width) so lexicographic
// key order matches numeric fileID order, which is what LookupEntity's PrefixScan relies
// on to return fileIDs in ascending order.
func entityKey(entityName string, fileID uint64) string {
	return fmt.Sprintf("%s%020d", entityKeyPrefix(entityName), fileID)
}

// PutEdge appends one occurrence of a graph edge (source_file_id -> target_file_id, of
// edge_type, with this call's own weight) to engine/graph's per-node edge log
// (graph.EdgeLog.AppendEdge). New scope: see this file's package doc comment above and
// .cdr/runs/2026-07-10/011-implementation/architecture-discovery.md.
//
// This handler deliberately does NOT compute or apply any weight-increment arithmetic
// itself: engine/graph.Compact (already implemented, task-3.1.3) is what sums Weight
// across repeated (source, target, ENTITY_COOCCUR) occurrences when it later folds the
// edge log into a fresh CSR snapshot (see compact.go's package doc comment,
// "Weight-aggregation semantics") -- every other edge type is deduplicated there to the
// most-recently-observed occurrence rather than summed. PutEdge's only job is to durably
// record one such occurrence; repeated PutEdge calls for the same (source, target,
// ENTITY_COOCCUR) triple are what "weight increments on repeated calls" (this task's
// acceptance criterion) means in practice, once Compact runs.
func (s *Server) PutEdge(ctx context.Context, req *hivemindv1.PutEdgeRequest) (*hivemindv1.PutEdgeResponse, error) {
	if s.edgeLog == nil {
		return nil, status.Error(codes.Unavailable, "rpc: PutEdge: server has no edge log configured")
	}

	sourceFileID := req.GetSourceFileId()
	if sourceFileID == catalog.InvalidFileID {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: PutEdge: source_file_id %d is invalid (proto3 zero-value / unset field)", sourceFileID)
	}
	targetFileID := req.GetTargetFileId()
	if targetFileID == catalog.InvalidFileID {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: PutEdge: target_file_id %d is invalid (proto3 zero-value / unset field)", targetFileID)
	}

	edgeType, err := protoEdgeTypeToGraph(req.GetEdgeType())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: PutEdge: %v", err)
	}
	if edgeType == graph.EdgeTypeInvalid {
		return nil, status.Error(codes.InvalidArgument, "rpc: PutEdge: edge_type must not be EDGE_TYPE_UNSPECIFIED")
	}

	weight := req.GetWeight()
	if weight == 0 {
		return nil, status.Error(codes.InvalidArgument, "rpc: PutEdge: weight must be > 0")
	}

	edge, err := graph.NewCSREdge(targetFileID, edgeType, weight, s.now().Unix())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rpc: PutEdge: %v", err)
	}

	if err := s.edgeLog.AppendEdge(sourceFileID, edge); err != nil {
		return nil, status.Errorf(codes.Internal, "rpc: PutEdge: %v", err)
	}

	return &hivemindv1.PutEdgeResponse{}, nil
}

// PutEntity registers file_id as associated with entity_name in the entity.idx concept
// (docs/LLD/ingestion-agent.md: "entities feed entity.idx"). New scope: see this file's
// package doc comment above.
//
// Storage mechanism: entity.idx is implemented as ordinary leaf entries in a dedicated
// engine/btree.Tree (Server.entityIndex), keyed by entityKey (above) -- reusing the
// existing B+Tree/PrefixScan primitive SearchCandidates already relies on, rather than
// inventing a new storage mechanism, per this task's design guidance. Re-registering the
// same (entity_name, file_id) pair is a harmless, idempotent upsert (btree.Tree.Insert's
// own upsert semantics: re-inserting an identical key/value pair is a no-op write).
func (s *Server) PutEntity(ctx context.Context, req *hivemindv1.PutEntityRequest) (*hivemindv1.PutEntityResponse, error) {
	if s.entityIndex == nil {
		return nil, status.Error(codes.Unavailable, "rpc: PutEntity: server has no entity index configured")
	}

	entityName := req.GetEntityName()
	if entityName == "" {
		return nil, status.Error(codes.InvalidArgument, "rpc: PutEntity: entity_name must not be empty")
	}
	fileID := req.GetFileId()
	if fileID == catalog.InvalidFileID {
		return nil, status.Errorf(codes.InvalidArgument, "rpc: PutEntity: file_id %d is invalid (proto3 zero-value / unset field)", fileID)
	}

	if err := s.entityIndex.Insert(entityKey(entityName, fileID), fileID); err != nil {
		return nil, status.Errorf(codes.Internal, "rpc: PutEntity: %v", err)
	}

	return &hivemindv1.PutEntityResponse{}, nil
}

// LookupEntity returns every file_id previously registered (via PutEntity) against
// entity_name, in ascending file_id order (see entityKey's doc comment for why
// zero-padding makes PrefixScan's lexicographic order match numeric fileID order). An
// entity_name with no registered files returns an empty (not error) result, mirroring
// SearchCandidates'/btree.PrefixScan's own not-found=empty-slice convention. New scope:
// see this file's package doc comment above.
func (s *Server) LookupEntity(ctx context.Context, req *hivemindv1.LookupEntityRequest) (*hivemindv1.LookupEntityResponse, error) {
	if s.entityIndex == nil {
		return nil, status.Error(codes.Unavailable, "rpc: LookupEntity: server has no entity index configured")
	}

	entityName := req.GetEntityName()
	if entityName == "" {
		return nil, status.Error(codes.InvalidArgument, "rpc: LookupEntity: entity_name must not be empty")
	}

	rootNodeID := s.entityIndex.Root()
	if rootNodeID == 0 {
		// 0 is btree's reservedNodeID sentinel for "this tree has never had anything
		// inserted into it" (see btree/lookup.go's reservedNodeID doc comment). Unlike
		// Lookup, PrefixScan does NOT special-case this root value itself (by design, per
		// its own doc comment) -- callers are expected to. An empty entity index means
		// entity_name (and every other name) simply has zero registered files, which is
		// LookupEntity's normal not-found outcome (empty result, nil error), not an error.
		return &hivemindv1.LookupEntityResponse{}, nil
	}

	entries, err := btree.PrefixScan(s.entityIndex.Store, rootNodeID, entityKeyPrefix(entityName))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rpc: LookupEntity: %v", err)
	}

	fileIDs := make([]uint64, len(entries))
	for i, e := range entries {
		fileIDs[i] = e.FileID
	}

	return &hivemindv1.LookupEntityResponse{FileIds: fileIDs}, nil
}
