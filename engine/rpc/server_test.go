package rpc

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/graph"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// fixture wires together catalog + content + btree + graph exactly as
// engine/integration_test.go's TestStorageCoreIntegration does, so server_test.go's fixture
// setup matches this repo's established real-module (not mocked) test composition.
type fixture struct {
	cat        *catalog.Catalog
	cs         *catalog.ContentStore
	idAlloc    *catalog.IDAllocator
	btreeStore *btree.NodeStore
	btreeRoot  uint64
	srv        *Server

	alphaID, betaID uint64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()

	fm, err := catalog.Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		t.Fatalf("catalog.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := fm.Close(); err != nil {
			t.Errorf("FileManager.Close: %v", err)
		}
	})

	cat := catalog.NewCatalog(fm)

	idAlloc, err := catalog.NewIDAllocator(fm)
	if err != nil {
		t.Fatalf("catalog.NewIDAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := idAlloc.Close(); err != nil {
			t.Errorf("IDAllocator.Close: %v", err)
		}
	})

	w, err := wal.OpenWriter(filepath.Join(root, "wal"), 1<<20)
	if err != nil {
		t.Fatalf("wal.OpenWriter: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("wal.Writer.Close: %v", err)
		}
	})

	cs, err := catalog.OpenContentStore(root, cat, w)
	if err != nil {
		t.Fatalf("catalog.OpenContentStore: %v", err)
	}

	idxFile, err := btree.OpenIndexFile(filepath.Join(root, "topics.idx"))
	if err != nil {
		t.Fatalf("btree.OpenIndexFile: %v", err)
	}
	t.Cleanup(func() {
		if err := idxFile.Close(); err != nil {
			t.Errorf("index file Close: %v", err)
		}
	})

	store := btree.NewNodeStore(idxFile)
	nodeAlloc, err := btree.NewNodeAllocator(store)
	if err != nil {
		t.Fatalf("btree.NewNodeAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := nodeAlloc.Close(); err != nil {
			t.Errorf("NodeAllocator.Close: %v", err)
		}
	})

	f := &fixture{cat: cat, cs: cs, idAlloc: idAlloc, btreeStore: store}

	// --- Seed two fixture topic files directly via ContentStore/Catalog. ---
	seed := func(content []byte) uint64 {
		fileID, err := idAlloc.Next()
		if err != nil {
			t.Fatalf("IDAllocator.Next: %v", err)
		}
		rec := catalog.CatalogRecord{
			FileID:         fileID,
			CurrentVersion: 1,
			SizeBytes:      uint64(len(content)),
			Status:         catalog.StatusActive,
		}
		if _, err := cs.Create(rec, content); err != nil {
			t.Fatalf("ContentStore.Create: %v", err)
		}
		return fileID
	}

	alphaContent := []byte("# Alpha\n\nintro text\n\n## Alpha Details\n\nmore text\n")
	alphaID := seed(alphaContent)

	betaContent := []byte("# Beta\n\nbeta body\n")
	betaID := seed(betaContent)

	// --- Seed the B+Tree with path -> fileID entries for SearchCandidates. ---
	rootNodeID := uint64(0)
	insertPath := func(path string, fileID uint64) {
		t.Helper()
		rec := wal.NewBTreeInsertRecord(path, fileID)
		if _, err := wal.AppendAndApply(w, rec, func() error {
			newRoot, err := btree.Insert(store, nodeAlloc, rootNodeID, path, fileID)
			if err != nil {
				return err
			}
			rootNodeID = newRoot
			return nil
		}); err != nil {
			t.Fatalf("insertPath(%q): %v", path, err)
		}
	}
	insertPath("topics/alpha/intro", alphaID)
	insertPath("topics/beta/intro", betaID)
	f.btreeRoot = rootNodeID

	// --- Seed a fixture CSR graph: alphaID -> betaID (ENTITY_COOCCUR) at hop 1. ---
	adjacency := map[uint64][]graph.CSREdge{
		alphaID: {
			{Target: betaID, Type: graph.EdgeEntityCooccur, Weight: 7, LastUpdated: 1000},
		},
	}
	g := graph.BuildCSR(adjacency)

	srv, err := NewServer(cat, cs, idAlloc, g, store, rootNodeID)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	f.srv = srv

	f.alphaID, f.betaID = alphaID, betaID
	return f
}

func TestRPCServerHandlers(t *testing.T) {
	t.Run("PutSegment_Create", func(t *testing.T) {
		f := newFixture(t)
		content := []byte("# New Topic\n\nbody\n")
		resp, err := f.srv.PutSegment(context.Background(), &hivemindv1.PutSegmentRequest{
			FileId:  0,
			Content: content,
		})
		if err != nil {
			t.Fatalf("PutSegment (create): %v", err)
		}
		if resp.GetFileId() == 0 {
			t.Fatalf("PutSegment (create): got FileId 0, want non-zero allocated fileID")
		}
		if resp.GetNewVersion() != 1 {
			t.Fatalf("PutSegment (create): got NewVersion %d, want 1", resp.GetNewVersion())
		}

		got, err := f.cs.Read(resp.GetFileId())
		if err != nil {
			t.Fatalf("ContentStore.Read after PutSegment create: %v", err)
		}
		if string(got) != string(content) {
			t.Fatalf("PutSegment (create): content mismatch: got %q, want %q", got, content)
		}
	})

	t.Run("PutSegment_Append", func(t *testing.T) {
		f := newFixture(t)
		createResp, err := f.srv.PutSegment(context.Background(), &hivemindv1.PutSegmentRequest{
			FileId:  0,
			Content: []byte("# T\n\nfirst\n"),
		})
		if err != nil {
			t.Fatalf("PutSegment (create): %v", err)
		}

		appendResp, err := f.srv.PutSegment(context.Background(), &hivemindv1.PutSegmentRequest{
			FileId:  createResp.GetFileId(),
			Content: []byte("second\n"),
		})
		if err != nil {
			t.Fatalf("PutSegment (append): %v", err)
		}
		if appendResp.GetFileId() != createResp.GetFileId() {
			t.Fatalf("PutSegment (append): FileId changed: got %d, want %d", appendResp.GetFileId(), createResp.GetFileId())
		}

		got, err := f.cs.Read(createResp.GetFileId())
		if err != nil {
			t.Fatalf("ContentStore.Read after PutSegment append: %v", err)
		}
		want := "# T\n\nfirst\nsecond\n"
		if string(got) != want {
			t.Fatalf("PutSegment (append): content mismatch: got %q, want %q", got, want)
		}
	})

	t.Run("PutSegment_Append_NotFound", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.PutSegment(context.Background(), &hivemindv1.PutSegmentRequest{
			FileId:  99999,
			Content: []byte("x"),
		})
		assertCode(t, err, codes.NotFound)
	})

	t.Run("GetFile", func(t *testing.T) {
		f := newFixture(t)
		wantContent, err := f.cs.Read(f.alphaID)
		if err != nil {
			t.Fatalf("ContentStore.Read (direct): %v", err)
		}
		wantRec, err := f.cat.Get(f.alphaID)
		if err != nil {
			t.Fatalf("Catalog.Get (direct): %v", err)
		}

		resp, err := f.srv.GetFile(context.Background(), &hivemindv1.GetFileRequest{FileId: f.alphaID})
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		if string(resp.GetContent()) != string(wantContent) {
			t.Fatalf("GetFile: content mismatch: got %q, want %q", resp.GetContent(), wantContent)
		}
		if resp.GetVersion() != wantRec.CurrentVersion {
			t.Fatalf("GetFile: version mismatch: got %d, want %d", resp.GetVersion(), wantRec.CurrentVersion)
		}
	})

	t.Run("GetFile_NotFound", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.GetFile(context.Background(), &hivemindv1.GetFileRequest{FileId: 99999})
		assertCode(t, err, codes.NotFound)
	})

	// GetFile_ZeroFileID: regression test for the FileId=0 (proto3 zero-value / unset
	// field) misclassification finding surfaced during task-3.2.2 verification
	// (.cdr/index/regression.jsonl, issue #16 / .cdr/runs/2026-07-09/003-verification) and
	// folded into task-3.2.4. FileId=0 is a plausible ordinary client mistake (forgetting
	// to set file_id), not an internal server fault, so it must map to
	// codes.InvalidArgument, not codes.Internal.
	t.Run("GetFile_ZeroFileID", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.GetFile(context.Background(), &hivemindv1.GetFileRequest{FileId: 0})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("ReadPartial", func(t *testing.T) {
		f := newFixture(t)
		want, err := f.cs.ReadPartial(f.alphaID)
		if err != nil {
			t.Fatalf("ContentStore.ReadPartial (direct): %v", err)
		}

		resp, err := f.srv.ReadPartial(context.Background(), &hivemindv1.ReadPartialRequest{FileId: f.alphaID})
		if err != nil {
			t.Fatalf("ReadPartial: %v", err)
		}
		if len(resp.GetHeaders()) != len(want) {
			t.Fatalf("ReadPartial: got %d headers, want %d", len(resp.GetHeaders()), len(want))
		}
		for i, h := range want {
			got := resp.GetHeaders()[i]
			if got.GetHeader() != h.Header || got.GetOffset() != int64(h.Offset) {
				t.Fatalf("ReadPartial: header[%d] = {%q, %d}, want {%q, %d}", i, got.GetHeader(), got.GetOffset(), h.Header, h.Offset)
			}
		}
	})

	t.Run("ReadPartial_NotFound", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.ReadPartial(context.Background(), &hivemindv1.ReadPartialRequest{FileId: 99999})
		assertCode(t, err, codes.NotFound)
	})

	// ReadPartial_ZeroFileID: regression test, see GetFile_ZeroFileID's doc comment above
	// for the finding this covers.
	t.Run("ReadPartial_ZeroFileID", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.ReadPartial(context.Background(), &hivemindv1.ReadPartialRequest{FileId: 0})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("GraphNeighbors", func(t *testing.T) {
		f := newFixture(t)
		want, err := graph.GraphNeighbors(f.srv.g, f.alphaID, 1, graph.EdgeTypeInvalid, 10)
		if err != nil {
			t.Fatalf("graph.GraphNeighbors (direct): %v", err)
		}

		resp, err := f.srv.GraphNeighbors(context.Background(), &hivemindv1.GraphNeighborsRequest{
			FileId:   f.alphaID,
			Depth:    1,
			MaxNodes: 10,
		})
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		if len(resp.GetNeighbors()) != len(want) {
			t.Fatalf("GraphNeighbors: got %d neighbors, want %d", len(resp.GetNeighbors()), len(want))
		}
		for i, e := range want {
			got := resp.GetNeighbors()[i]
			if got.GetTargetFileId() != e.Target || got.GetWeight() != e.Weight {
				t.Fatalf("GraphNeighbors: neighbor[%d] = {target %d, weight %d}, want {target %d, weight %d}",
					i, got.GetTargetFileId(), got.GetWeight(), e.Target, e.Weight)
			}
			wantProtoType, err := graphEdgeTypeToProto(e.Type)
			if err != nil {
				t.Fatalf("graphEdgeTypeToProto: %v", err)
			}
			if got.GetType() != wantProtoType {
				t.Fatalf("GraphNeighbors: neighbor[%d].Type = %v, want %v", i, got.GetType(), wantProtoType)
			}
		}
	})

	t.Run("GraphNeighbors_InvalidDepth", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.GraphNeighbors(context.Background(), &hivemindv1.GraphNeighborsRequest{
			FileId: f.alphaID,
			Depth:  3,
		})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("GraphNeighbors_InvalidMaxNodes", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.GraphNeighbors(context.Background(), &hivemindv1.GraphNeighborsRequest{
			FileId:   f.alphaID,
			Depth:    1,
			MaxNodes: -1,
		})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("SearchCandidates", func(t *testing.T) {
		f := newFixture(t)
		want, err := btree.PrefixScan(f.btreeStore, f.btreeRoot, "topics/alpha/")
		if err != nil {
			t.Fatalf("btree.PrefixScan (direct): %v", err)
		}

		resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
			Query: "topics/alpha/",
		})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}
		if len(resp.GetCandidates()) != len(want) {
			t.Fatalf("SearchCandidates: got %d candidates, want %d", len(resp.GetCandidates()), len(want))
		}
		for i, e := range want {
			got := resp.GetCandidates()[i]
			if got.GetFileId() != e.FileID || got.GetPath() != e.Path {
				t.Fatalf("SearchCandidates: candidate[%d] = {%d, %q}, want {%d, %q}",
					i, got.GetFileId(), got.GetPath(), e.FileID, e.Path)
			}
		}
	})

	t.Run("SearchCandidates_MaxResultsCap", func(t *testing.T) {
		f := newFixture(t)
		resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
			Query:      "topics/",
			MaxResults: 1,
		})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}
		if len(resp.GetCandidates()) != 1 {
			t.Fatalf("SearchCandidates: got %d candidates, want 1 (capped)", len(resp.GetCandidates()))
		}
	})

	t.Run("SearchCandidates_NoMatches", func(t *testing.T) {
		f := newFixture(t)
		resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
			Query: "no/such/prefix",
		})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}
		if len(resp.GetCandidates()) != 0 {
			t.Fatalf("SearchCandidates: got %d candidates, want 0", len(resp.GetCandidates()))
		}
	})

	t.Run("ProposeSplit_Unimplemented", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.ProposeSplit(context.Background(), &hivemindv1.ProposeSplitRequest{FileContent: []byte("x")})
		assertCode(t, err, codes.Unimplemented)
	})

	t.Run("Concurrent", func(t *testing.T) {
		f := newFixture(t)
		const n = 32
		var wg sync.WaitGroup
		errs := make(chan error, n*2)
		for i := 0; i < n; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				if _, err := f.srv.GetFile(context.Background(), &hivemindv1.GetFileRequest{FileId: f.alphaID}); err != nil {
					errs <- err
				}
			}()
			go func() {
				defer wg.Done()
				if _, err := f.srv.GraphNeighbors(context.Background(), &hivemindv1.GraphNeighborsRequest{
					FileId: f.alphaID, Depth: 1, MaxNodes: 10,
				}); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("concurrent call error: %v", err)
		}
	})
}

// TestEdgeTypeConversionRoundTrip guards the proto<->graph EdgeType numeric mismatch found
// during architecture-discovery (see server.go's protoEdgeTypeToGraph doc comment): the two
// enums' iota orders do not line up, so this asserts the explicit name-based conversion
// functions round-trip correctly for every valid enum value.
func TestEdgeTypeConversionRoundTrip(t *testing.T) {
	cases := []struct {
		proto hivemindv1.EdgeType
		graph graph.EdgeType
	}{
		{hivemindv1.EdgeType_EDGE_TYPE_UNSPECIFIED, graph.EdgeTypeInvalid},
		{hivemindv1.EdgeType_ENTITY_COOCCUR, graph.EdgeEntityCooccur},
		{hivemindv1.EdgeType_LLM_ASSERTED, graph.EdgeLLMAsserted},
		{hivemindv1.EdgeType_SPLIT_SIBLING, graph.EdgeSplitSibling},
		{hivemindv1.EdgeType_REDIRECT, graph.EdgeRedirect},
	}
	for _, c := range cases {
		gotGraph, err := protoEdgeTypeToGraph(c.proto)
		if err != nil {
			t.Fatalf("protoEdgeTypeToGraph(%v): %v", c.proto, err)
		}
		if gotGraph != c.graph {
			t.Fatalf("protoEdgeTypeToGraph(%v) = %v, want %v", c.proto, gotGraph, c.graph)
		}

		if c.proto == hivemindv1.EdgeType_EDGE_TYPE_UNSPECIFIED {
			// graphEdgeTypeToProto has no inverse for EdgeTypeInvalid (GraphNeighbors
			// results never carry EdgeTypeInvalid -- it's a "no filter" sentinel on the
			// request side only, never a real edge's Type). Skip the reverse leg for this
			// case only.
			continue
		}
		gotProto, err := graphEdgeTypeToProto(c.graph)
		if err != nil {
			t.Fatalf("graphEdgeTypeToProto(%v): %v", c.graph, err)
		}
		if gotProto != c.proto {
			t.Fatalf("graphEdgeTypeToProto(%v) = %v, want %v", c.graph, gotProto, c.proto)
		}
	}
}

func assertCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want status code %v", want)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("got non-status error %v, want status code %v", err, want)
	}
	if st.Code() != want {
		t.Fatalf("got status code %v, want %v (err: %v)", st.Code(), want, err)
	}
}
