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
	cat       *catalog.Catalog
	cs        *catalog.ContentStore
	idAlloc   *catalog.IDAllocator
	pathIndex *btree.Tree
	srv       *Server

	// edgeLog/graphPath back PutEdge; entityIndex backs PutEntity/LookupEntity. Both are
	// wholly separate from pathIndex above -- see server.go's Server.entityIndex doc
	// comment for why.
	edgeLog     *graph.EdgeLog
	graphPath   string
	entityIndex *btree.Tree

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

	f := &fixture{cat: cat, cs: cs, idAlloc: idAlloc}

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
	pathIndex := btree.NewTree(store, nodeAlloc, rootNodeID)
	f.pathIndex = pathIndex

	// --- Seed a fixture CSR graph: alphaID -> betaID (ENTITY_COOCCUR) at hop 1. ---
	adjacency := map[uint64][]graph.CSREdge{
		alphaID: {
			{Target: betaID, Type: graph.EdgeEntityCooccur, Weight: 7, LastUpdated: 1000},
		},
	}
	g := graph.BuildCSR(adjacency)

	// --- Separate edge log + entity-index tree (new scope: PutEdge/PutEntity/LookupEntity). ---
	edgeLog, err := graph.OpenEdgeLog(filepath.Join(root, "edgelog"))
	if err != nil {
		t.Fatalf("graph.OpenEdgeLog: %v", err)
	}
	t.Cleanup(func() {
		if err := edgeLog.Close(); err != nil {
			t.Errorf("EdgeLog.Close: %v", err)
		}
	})
	f.edgeLog = edgeLog
	f.graphPath = filepath.Join(root, "graph.dat")

	entityIdxFile, err := btree.OpenIndexFile(filepath.Join(root, "entity.idx"))
	if err != nil {
		t.Fatalf("btree.OpenIndexFile (entity index): %v", err)
	}
	t.Cleanup(func() {
		if err := entityIdxFile.Close(); err != nil {
			t.Errorf("entity index file Close: %v", err)
		}
	})
	entityStore := btree.NewNodeStore(entityIdxFile)
	entityAlloc, err := btree.NewNodeAllocator(entityStore)
	if err != nil {
		t.Fatalf("btree.NewNodeAllocator (entity index): %v", err)
	}
	t.Cleanup(func() {
		if err := entityAlloc.Close(); err != nil {
			t.Errorf("entity index NodeAllocator.Close: %v", err)
		}
	})
	entityIndex := btree.NewTree(entityStore, entityAlloc, 0)
	f.entityIndex = entityIndex

	srv, err := NewServer(cat, cs, idAlloc, g, pathIndex, edgeLog, entityIndex)
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

	t.Run("PutSegment_Create_WithPath_SetsPathHashAndIndexesForSearch", func(t *testing.T) {
		// Issue #43, commit 2/3: a create with path must (a) set CatalogRecord.PathHash to
		// catalog.HashPath(path), and (b) insert (path, fileID) into pathIndex -- the exact
		// same tree SearchCandidates reads from -- not just one or the other. See
		// engine/rpc/integration_test.go's PutSegment_Create_DiscoverableViaSearchCandidates
		// for the full end-to-end (real gRPC transport) version of this same assertion.
		f := newFixture(t)
		const path = "unit/pathhash-check"
		content := []byte("# Unit\n\nbody\n")
		resp, err := f.srv.PutSegment(context.Background(), &hivemindv1.PutSegmentRequest{
			FileId:  0,
			Content: content,
			Path:    path,
		})
		if err != nil {
			t.Fatalf("PutSegment (create with path): %v", err)
		}

		rec, err := f.cat.Get(resp.GetFileId())
		if err != nil {
			t.Fatalf("catalog.Get after PutSegment create: %v", err)
		}
		if want := catalog.HashPath(path); rec.PathHash != want {
			t.Fatalf("PutSegment (create with path): CatalogRecord.PathHash = %d, want %d (catalog.HashPath(%q))", rec.PathHash, want, path)
		}

		entries, err := btree.PrefixScan(f.pathIndex.Store, f.pathIndex.Root(), path)
		if err != nil {
			t.Fatalf("btree.PrefixScan (direct, after PutSegment create): %v", err)
		}
		var foundInIndex bool
		for _, e := range entries {
			if e.Path == path && e.FileID == resp.GetFileId() {
				foundInIndex = true
			}
		}
		if !foundInIndex {
			t.Fatalf("PutSegment (create with path): pathIndex has no (path=%q, fileID=%d) entry after create (entries=%v) -- issue #43 regression", path, resp.GetFileId(), entries)
		}
	})

	t.Run("PutSegment_Create_NoPath_LeavesPathHashZeroAndSkipsIndexing", func(t *testing.T) {
		// A caller that omits path (this handler's pre-issue-#43 call shape) must be
		// completely unaffected: PathHash stays at its zero-value default and nothing is
		// inserted into pathIndex.
		f := newFixture(t)
		resp, err := f.srv.PutSegment(context.Background(), &hivemindv1.PutSegmentRequest{
			FileId:  0,
			Content: []byte("# NoPath\n\nbody\n"),
		})
		if err != nil {
			t.Fatalf("PutSegment (create, no path): %v", err)
		}

		rec, err := f.cat.Get(resp.GetFileId())
		if err != nil {
			t.Fatalf("catalog.Get after PutSegment create (no path): %v", err)
		}
		if rec.PathHash != 0 {
			t.Fatalf("PutSegment (create, no path): CatalogRecord.PathHash = %d, want 0 (unset)", rec.PathHash)
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
		// Regression test for GitHub issue #56 subtask 4.6.3.2: GetFileResponse.path is a
		// reverse pathIndex lookup, populated for any fileID indexed via PutSegment/
		// insertPath, not just fileIDs already present in a SearchCandidates result.
		if resp.GetPath() != "topics/alpha/intro" {
			t.Fatalf("GetFile: path mismatch: got %q, want %q", resp.GetPath(), "topics/alpha/intro")
		}
	})

	// GetFile_PathIndexMiss: regression test for GitHub issue #56 subtask 4.6.3.2's
	// lookupPathForFileID -- a fileID with no pathIndex entry (e.g. it was seeded directly
	// via ContentStore/Catalog, bypassing PutSegment/insertPath, as betaID's
	// topics/beta/intro entry always is here since insertPath is only ever called for it,
	// never skipped -- so this test uses a *different*, never-indexed fileID) must return
	// path == "" (proto3 zero-value), not error.
	t.Run("GetFile_PathIndexMiss", func(t *testing.T) {
		f := newFixture(t)
		unindexedID, err := f.idAlloc.Next()
		if err != nil {
			t.Fatalf("IDAllocator.Next: %v", err)
		}
		rec := catalog.CatalogRecord{
			FileID:         unindexedID,
			CurrentVersion: 1,
			SizeBytes:      5,
			Status:         catalog.StatusActive,
		}
		if _, err := f.cs.Create(rec, []byte("hello")); err != nil {
			t.Fatalf("ContentStore.Create: %v", err)
		}

		resp, err := f.srv.GetFile(context.Background(), &hivemindv1.GetFileRequest{FileId: unindexedID})
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		if resp.GetPath() != "" {
			t.Fatalf("GetFile: path mismatch for unindexed fileID: got %q, want empty", resp.GetPath())
		}
	})

	// GetFile_NilPathIndex: regression test for GitHub issue #56 subtask 4.6.3.2 -- a
	// Server constructed with a nil pathIndex (a documented-valid configuration, see
	// Server.pathIndex's field doc) must still answer GetFile successfully, with path == ""
	// rather than erroring.
	t.Run("GetFile_NilPathIndex", func(t *testing.T) {
		f := newFixture(t)
		srv, err := NewServer(f.cat, f.cs, f.idAlloc, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		resp, err := srv.GetFile(context.Background(), &hivemindv1.GetFileRequest{FileId: f.alphaID})
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		if resp.GetPath() != "" {
			t.Fatalf("GetFile: path mismatch with nil pathIndex: got %q, want empty", resp.GetPath())
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

		// task 4.5.9.2 (issue #47) changed pool assembly from a single literal
		// PrefixScan(req.Query) call to one PrefixScan PER TERM of req.Query, split via
		// the SAME non-alphanumeric-run convention rankCandidates' tokenizeTerms already
		// uses for scoring (splitTerms/termSplitRE, search_candidates.go) -- resolving a
		// pre-existing inconsistency where pool selection treated "topics/alpha/" as one
		// literal path-prefix token while ranking already tokenized it into ["topics",
		// "alpha"]. "topics/alpha/" therefore now splits into terms ["topics", "alpha"];
		// PrefixScan("topics") matches BOTH seeded paths (topics/alpha/intro AND
		// topics/beta/intro, both sharing the "topics" prefix), while PrefixScan("alpha")
		// matches neither literally (no path starts with "alpha"). The merged pool is
		// therefore both entries, but rankCandidates' term-overlap scoring still ranks
		// topics/alpha/intro first (2-of-2 terms match its path tokens) ahead of
		// topics/beta/intro (1-of-2) -- broader recall than the pre-4.5.9.2 behavior
		// (which silently excluded topics/beta/intro from the pool entirely), still
		// correctly ranked.
		resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{
			Query: "topics/alpha/",
		})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}

		got := resp.GetCandidates()
		if len(got) != 2 {
			t.Fatalf("SearchCandidates: got %d candidates, want 2 (topics/alpha/intro and topics/beta/intro, both sharing the \"topics\" scan term)", len(got))
		}
		if got[0].GetPath() != "topics/alpha/intro" || got[0].GetFileId() != f.alphaID {
			t.Fatalf("SearchCandidates: candidate[0] = {%d, %q}, want {%d, %q} (highest term-overlap: matches both \"topics\" and \"alpha\")",
				got[0].GetFileId(), got[0].GetPath(), f.alphaID, "topics/alpha/intro")
		}
		if got[1].GetPath() != "topics/beta/intro" || got[1].GetFileId() != f.betaID {
			t.Fatalf("SearchCandidates: candidate[1] = {%d, %q}, want {%d, %q} (lower term-overlap: matches only \"topics\")",
				got[1].GetFileId(), got[1].GetPath(), f.betaID, "topics/beta/intro")
		}
		if !(got[0].GetScore() > got[1].GetScore()) {
			t.Fatalf("SearchCandidates: candidate[0] score %v not greater than candidate[1] score %v", got[0].GetScore(), got[1].GetScore())
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

// TestPutEdgeAndEntityHandlers covers the new scope added during issue #18 subtask 3.4.4's
// verification (see .cdr/runs/2026-07-10/011-implementation/): PutEdge (edge-log append,
// weight-increment semantics verified end-to-end via graph.Compact) and
// PutEntity/LookupEntity (entity.idx round trip, backed by a dedicated B+Tree).
func TestPutEdgeAndEntityHandlers(t *testing.T) {
	t.Run("PutEdge_Create", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.PutEdge(context.Background(), &hivemindv1.PutEdgeRequest{
			SourceFileId: f.alphaID,
			TargetFileId: f.betaID,
			EdgeType:     hivemindv1.EdgeType_LLM_ASSERTED,
			Weight:       1,
		})
		if err != nil {
			t.Fatalf("PutEdge: %v", err)
		}

		edges, err := f.edgeLog.ReadNode(f.alphaID)
		if err != nil {
			t.Fatalf("EdgeLog.ReadNode: %v", err)
		}
		if len(edges) != 1 {
			t.Fatalf("EdgeLog.ReadNode: got %d edges, want 1", len(edges))
		}
		if edges[0].Target != f.betaID || edges[0].Type != graph.EdgeLLMAsserted || edges[0].Weight != 1 {
			t.Fatalf("EdgeLog.ReadNode: got %+v, want Target=%d Type=%v Weight=1", edges[0], f.betaID, graph.EdgeLLMAsserted)
		}
	})

	t.Run("PutEdge_WeightIncrement_ViaCompact", func(t *testing.T) {
		f := newFixture(t)
		// Distinct weights (3, 4, 5), not identical (1, 1, 1): with identical
		// weights, a "sum" result (3) and a "count" result (also 3, since
		// count*1 == sum(1,1,1)) are indistinguishable, so the assertion below
		// couldn't actually discriminate graph.Compact/mergeEdges' true summing
		// behavior from an accidental count-only implementation. 3+4+5=12 is
		// reachable only by true summation -- not by count (3), max (5), or
		// last-write-wins (5) -- so it uniquely discriminates sum semantics.
		weights := []uint32{3, 4, 5}
		for i, weight := range weights {
			_, err := f.srv.PutEdge(context.Background(), &hivemindv1.PutEdgeRequest{
				SourceFileId: f.alphaID,
				TargetFileId: f.betaID,
				EdgeType:     hivemindv1.EdgeType_ENTITY_COOCCUR,
				Weight:       weight,
			})
			if err != nil {
				t.Fatalf("PutEdge call %d: %v", i, err)
			}
		}

		compacted, err := graph.Compact(f.graphPath, f.edgeLog)
		if err != nil {
			t.Fatalf("graph.Compact: %v", err)
		}

		neighbors := compacted.Neighbors(f.alphaID)
		var found bool
		for _, e := range neighbors {
			if e.Target == f.betaID && e.Type == graph.EdgeEntityCooccur {
				found = true
				// 3 PutEdge calls, weights 3+4+5, into f.graphPath -- a fresh path distinct
				// from the fixture's in-memory adjacency (f.srv's graph.CSRGraph), so there
				// is no pre-existing on-disk snapshot for Compact to fold into: the summed
				// result is exactly the 3 fresh log occurrences' weights added together.
				if e.Weight != 12 {
					t.Fatalf("Compact: ENTITY_COOCCUR weight = %d, want 12 (summed across 3 PutEdge calls of weight 3, 4, 5)", e.Weight)
				}
			}
		}
		if !found {
			t.Fatalf("Compact: no ENTITY_COOCCUR edge %d->%d found in %+v", f.alphaID, f.betaID, neighbors)
		}
	})

	t.Run("PutEdge_LLMAsserted_LastWriteWins", func(t *testing.T) {
		f := newFixture(t)
		for i := 0; i < 2; i++ {
			_, err := f.srv.PutEdge(context.Background(), &hivemindv1.PutEdgeRequest{
				SourceFileId: f.alphaID,
				TargetFileId: f.betaID,
				EdgeType:     hivemindv1.EdgeType_LLM_ASSERTED,
				Weight:       1,
			})
			if err != nil {
				t.Fatalf("PutEdge call %d: %v", i, err)
			}
		}

		compacted, err := graph.Compact(f.graphPath, f.edgeLog)
		if err != nil {
			t.Fatalf("graph.Compact: %v", err)
		}

		var count int
		for _, e := range compacted.Neighbors(f.alphaID) {
			if e.Target == f.betaID && e.Type == graph.EdgeLLMAsserted {
				count++
				if e.Weight != 1 {
					t.Fatalf("Compact: LLM_ASSERTED weight = %d, want 1 (deduplicated, not summed)", e.Weight)
				}
			}
		}
		if count != 1 {
			t.Fatalf("Compact: got %d LLM_ASSERTED edges %d->%d, want exactly 1 (deduplicated)", count, f.alphaID, f.betaID)
		}
	})

	t.Run("PutEdge_ZeroSourceFileID", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.PutEdge(context.Background(), &hivemindv1.PutEdgeRequest{
			SourceFileId: 0,
			TargetFileId: f.betaID,
			EdgeType:     hivemindv1.EdgeType_LLM_ASSERTED,
			Weight:       1,
		})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("PutEdge_ZeroTargetFileID", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.PutEdge(context.Background(), &hivemindv1.PutEdgeRequest{
			SourceFileId: f.alphaID,
			TargetFileId: 0,
			EdgeType:     hivemindv1.EdgeType_LLM_ASSERTED,
			Weight:       1,
		})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("PutEdge_UnspecifiedEdgeType", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.PutEdge(context.Background(), &hivemindv1.PutEdgeRequest{
			SourceFileId: f.alphaID,
			TargetFileId: f.betaID,
			EdgeType:     hivemindv1.EdgeType_EDGE_TYPE_UNSPECIFIED,
			Weight:       1,
		})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("PutEdge_ZeroWeight", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.PutEdge(context.Background(), &hivemindv1.PutEdgeRequest{
			SourceFileId: f.alphaID,
			TargetFileId: f.betaID,
			EdgeType:     hivemindv1.EdgeType_LLM_ASSERTED,
			Weight:       0,
		})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("PutEdge_NilEdgeLog", func(t *testing.T) {
		f := newFixture(t)
		f.srv.edgeLog = nil
		_, err := f.srv.PutEdge(context.Background(), &hivemindv1.PutEdgeRequest{
			SourceFileId: f.alphaID,
			TargetFileId: f.betaID,
			EdgeType:     hivemindv1.EdgeType_LLM_ASSERTED,
			Weight:       1,
		})
		assertCode(t, err, codes.Unavailable)
	})

	t.Run("PutEntity_LookupEntity_SingleFile", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.PutEntity(context.Background(), &hivemindv1.PutEntityRequest{
			EntityName: "acme-corp",
			FileId:     f.alphaID,
		})
		if err != nil {
			t.Fatalf("PutEntity: %v", err)
		}

		resp, err := f.srv.LookupEntity(context.Background(), &hivemindv1.LookupEntityRequest{EntityName: "acme-corp"})
		if err != nil {
			t.Fatalf("LookupEntity: %v", err)
		}
		if len(resp.GetFileIds()) != 1 || resp.GetFileIds()[0] != f.alphaID {
			t.Fatalf("LookupEntity: got %v, want [%d]", resp.GetFileIds(), f.alphaID)
		}
	})

	t.Run("PutEntity_Idempotent", func(t *testing.T) {
		f := newFixture(t)
		for i := 0; i < 2; i++ {
			_, err := f.srv.PutEntity(context.Background(), &hivemindv1.PutEntityRequest{
				EntityName: "acme-corp",
				FileId:     f.alphaID,
			})
			if err != nil {
				t.Fatalf("PutEntity call %d: %v", i, err)
			}
		}
		resp, err := f.srv.LookupEntity(context.Background(), &hivemindv1.LookupEntityRequest{EntityName: "acme-corp"})
		if err != nil {
			t.Fatalf("LookupEntity: %v", err)
		}
		if len(resp.GetFileIds()) != 1 {
			t.Fatalf("LookupEntity: got %v, want exactly 1 entry (idempotent re-registration)", resp.GetFileIds())
		}
	})

	t.Run("PutEntity_MultipleFiles", func(t *testing.T) {
		f := newFixture(t)
		// Insert in descending fileID order to confirm LookupEntity returns ascending
		// order regardless of insertion order (zero-padded key encoding, not insertion
		// order, determines PrefixScan's returned order).
		for _, id := range []uint64{f.betaID, f.alphaID} {
			_, err := f.srv.PutEntity(context.Background(), &hivemindv1.PutEntityRequest{
				EntityName: "shared-entity",
				FileId:     id,
			})
			if err != nil {
				t.Fatalf("PutEntity(%d): %v", id, err)
			}
		}
		resp, err := f.srv.LookupEntity(context.Background(), &hivemindv1.LookupEntityRequest{EntityName: "shared-entity"})
		if err != nil {
			t.Fatalf("LookupEntity: %v", err)
		}
		got := resp.GetFileIds()
		if len(got) != 2 {
			t.Fatalf("LookupEntity: got %v, want 2 entries", got)
		}
		lo, hi := f.alphaID, f.betaID
		if lo > hi {
			lo, hi = hi, lo
		}
		if got[0] != lo || got[1] != hi {
			t.Fatalf("LookupEntity: got %v, want ascending [%d, %d]", got, lo, hi)
		}
	})

	t.Run("LookupEntity_NotFound", func(t *testing.T) {
		f := newFixture(t)
		resp, err := f.srv.LookupEntity(context.Background(), &hivemindv1.LookupEntityRequest{EntityName: "never-registered"})
		if err != nil {
			t.Fatalf("LookupEntity: %v", err)
		}
		if len(resp.GetFileIds()) != 0 {
			t.Fatalf("LookupEntity: got %v, want empty", resp.GetFileIds())
		}
	})

	t.Run("PutEntity_EmptyName", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.PutEntity(context.Background(), &hivemindv1.PutEntityRequest{EntityName: "", FileId: f.alphaID})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("LookupEntity_EmptyName", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.LookupEntity(context.Background(), &hivemindv1.LookupEntityRequest{EntityName: ""})
		assertCode(t, err, codes.InvalidArgument)
	})

	t.Run("PutEntity_NilEntityIndex", func(t *testing.T) {
		f := newFixture(t)
		f.srv.entityIndex = nil
		_, err := f.srv.PutEntity(context.Background(), &hivemindv1.PutEntityRequest{EntityName: "x", FileId: f.alphaID})
		assertCode(t, err, codes.Unavailable)
	})

	t.Run("LookupEntity_NilEntityIndex", func(t *testing.T) {
		f := newFixture(t)
		f.srv.entityIndex = nil
		_, err := f.srv.LookupEntity(context.Background(), &hivemindv1.LookupEntityRequest{EntityName: "x"})
		assertCode(t, err, codes.Unavailable)
	})

	t.Run("EntityIndex_DoesNotLeakIntoSearchCandidates", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.srv.PutEntity(context.Background(), &hivemindv1.PutEntityRequest{
			EntityName: "topics", // deliberately chosen to share a prefix with the seeded
			// "topics/alpha/intro" / "topics/beta/intro" path-index entries, to prove
			// namespace isolation is real, not just "different prefix, never tested".
			FileId: f.alphaID,
		})
		if err != nil {
			t.Fatalf("PutEntity: %v", err)
		}

		resp, err := f.srv.SearchCandidates(context.Background(), &hivemindv1.SearchCandidatesRequest{Query: "topics"})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}
		for _, c := range resp.GetCandidates() {
			if c.GetPath() == "" {
				t.Fatalf("SearchCandidates: got a candidate with empty path %+v -- entity-index key leaked into path search", c)
			}
		}
		if len(resp.GetCandidates()) != 2 {
			t.Fatalf("SearchCandidates: got %d candidates, want exactly the 2 pre-seeded topic paths (entity index must not add/remove results)", len(resp.GetCandidates()))
		}
	})
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
