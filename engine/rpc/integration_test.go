// Package rpc_test (this file): task-3.2.5's cross-process gRPC integration test (GitHub
// issue #16, Epic Phase 3, final subtask of the 3.2.x sequence). Unlike server_test.go
// (which calls Server's handler methods directly, no transport at all) and
// interceptor_test.go / engine/split/proposer_grpc_test.go (both bufconn-based in-process
// transports), this file starts a real *grpc.Server bound to a real OS-level net.Listener
// (127.0.0.1, ephemeral port) with rpc.LatencyInterceptor genuinely wired in via
// grpc.UnaryInterceptor -- the first call site anywhere in this repo, test or production,
// that does so. See this run's architecture-discovery.md
// (.cdr/runs/2026-07-09/011-implementation/) for the grep evidence establishing that no
// prior production wiring site exists.
//
// Scope, per issue #16 task-3.2.5's own "Impacted modules: engine/rpc/integration_test.go"
// and this dispatch's explicit scope correction: exercises the real Go engine stack only
// (Server's 5 implemented RPCs -- PutSegment, GetFile, ReadPartial, GraphNeighbors,
// SearchCandidates -- against real catalog/content/btree/graph state) plus
// engine/split.GRPCSplitProposer (task-3.2.3's real gRPC client) issuing ProposeSplit
// against this same real server, confirming the client correctly surfaces
// codes.Unimplemented end-to-end. It deliberately does NOT implement, mock, or stand up any
// Python agent service: ProposeSplit's server side remains engine/rpc/server.go's inherited
// hivemindv1.UnimplementedHiveMindServer default by design (that is issue #18's scope, per
// docs/LLD/rpc.md's Status note and server.go's own doc comment).
package rpc_test

import (
	"context"
	"net"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/graph"
	"github.com/Aaryan123456679/HiveMind/engine/rpc"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
	"github.com/Aaryan123456679/HiveMind/engine/split"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// recordingRecorder is a test-only rpc.Recorder that captures every RPCMetric emitted by
// LatencyInterceptor, safe for concurrent use (unary RPCs are served concurrently by the
// gRPC runtime; this test's own subtests also run calls that could plausibly overlap under
// -race, so this must genuinely be synchronized, not just documented as if it were).
type recordingRecorder struct {
	mu      sync.Mutex
	metrics []rpc.RPCMetric
}

func (r *recordingRecorder) Record(m rpc.RPCMetric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = append(r.metrics, m)
}

func (r *recordingRecorder) snapshot() []rpc.RPCMetric {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]rpc.RPCMetric, len(r.metrics))
	copy(out, r.metrics)
	return out
}

// integrationFixture wires together real catalog + content + btree + graph state, exactly
// mirroring engine/rpc/server_test.go's newFixture and engine/integration_test.go's
// TestStorageCoreIntegration -- adapted to this file's external rpc_test package, which only
// has access to exported identifiers.
type integrationFixture struct {
	cs         *catalog.ContentStore
	btreeStore *btree.NodeStore
	btreeRoot  uint64

	client   hivemindv1.HiveMindClient
	recorder *recordingRecorder

	alphaID, betaID uint64
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
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

	// --- Seed a real CSR graph: alphaID -> betaID (ENTITY_COOCCUR). ---
	adjacency := map[uint64][]graph.CSREdge{
		alphaID: {
			{Target: betaID, Type: graph.EdgeEntityCooccur, Weight: 7, LastUpdated: 1000},
		},
	}
	g := graph.BuildCSR(adjacency)

	srv, err := rpc.NewServer(cat, cs, idAlloc, g, store, rootNodeID)
	if err != nil {
		t.Fatalf("rpc.NewServer: %v", err)
	}

	client, rec := startRealServer(t, srv)

	return &integrationFixture{
		cs:         cs,
		btreeStore: store,
		btreeRoot:  rootNodeID,
		client:     client,
		recorder:   rec,
		alphaID:    alphaID,
		betaID:     betaID,
	}
}

// startRealServer starts a real *grpc.Server, bound to a real net.Listener on
// 127.0.0.1:0 (an OS-assigned ephemeral loopback port -- never a fixed port, so this test
// cannot flake on port contention), with rpc.LatencyInterceptor genuinely wired in via
// grpc.UnaryInterceptor, serving srv as the registered hivemindv1.HiveMindServer. It returns
// a real hivemindv1.HiveMindClient dialed against that listener over an actual TCP
// connection, plus the recordingRecorder every call's RPCMetric is delivered to. Both the
// server and the client connection are torn down via t.Cleanup, matching
// engine/split/proposer_grpc_test.go's established -race-safe teardown pattern.
func startRealServer(t *testing.T, srv *rpc.Server) (hivemindv1.HiveMindClient, *recordingRecorder) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	rec := &recordingRecorder{}
	gsrv := grpc.NewServer(grpc.UnaryInterceptor(rpc.LatencyInterceptor(rpc.WithRecorder(rec))))
	hivemindv1.RegisterHiveMindServer(gsrv, srv)

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- gsrv.Serve(lis)
	}()
	t.Cleanup(func() {
		gsrv.GracefulStop()
		<-serveErrCh
	})

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	return hivemindv1.NewHiveMindClient(conn), rec
}

// TestRPCIntegration is task-3.2.5's required cross-process gRPC integration test: a
// realistic multi-RPC workflow driven entirely through a real client connection against one
// running real *grpc.Server instance and real backing catalog/content/graph/btree state,
// mirroring engine/integration_test.go's no-mocks composition style.
func TestRPCIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx := context.Background()

	var createdFileID uint64

	t.Run("PutSegment_Create", func(t *testing.T) {
		content := []byte("# New Topic\n\nbody\n")
		resp, err := f.client.PutSegment(ctx, &hivemindv1.PutSegmentRequest{
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
		createdFileID = resp.GetFileId()

		got, err := f.cs.Read(createdFileID)
		if err != nil {
			t.Fatalf("ContentStore.Read after PutSegment create: %v", err)
		}
		if string(got) != string(content) {
			t.Fatalf("PutSegment (create): content mismatch: got %q, want %q", got, content)
		}
	})

	t.Run("PutSegment_Append", func(t *testing.T) {
		if createdFileID == 0 {
			t.Fatal("PutSegment_Append: depends on PutSegment_Create having run first")
		}
		appendResp, err := f.client.PutSegment(ctx, &hivemindv1.PutSegmentRequest{
			FileId:  createdFileID,
			Content: []byte("second\n"),
		})
		if err != nil {
			t.Fatalf("PutSegment (append): %v", err)
		}
		if appendResp.GetFileId() != createdFileID {
			t.Fatalf("PutSegment (append): FileId changed: got %d, want %d", appendResp.GetFileId(), createdFileID)
		}
		// Note: CurrentVersion is not bumped by ContentStore.Append (append is a
		// same-version content mutation, not a new version) -- consistent with
		// engine/rpc/server_test.go's PutSegment_Append subtest, which likewise does not
		// assert a specific NewVersion after append.
	})

	t.Run("GetFile", func(t *testing.T) {
		if createdFileID == 0 {
			t.Fatal("GetFile: depends on PutSegment_Create having run first")
		}
		resp, err := f.client.GetFile(ctx, &hivemindv1.GetFileRequest{FileId: createdFileID})
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		want := "# New Topic\n\nbody\nsecond\n"
		if string(resp.GetContent()) != want {
			t.Fatalf("GetFile: got %q, want %q", resp.GetContent(), want)
		}

		// Cross-check against the real backing ContentStore directly.
		direct, err := f.cs.Read(createdFileID)
		if err != nil {
			t.Fatalf("ContentStore.Read (cross-check): %v", err)
		}
		if string(direct) != want {
			t.Fatalf("ContentStore.Read (cross-check): got %q, want %q", direct, want)
		}
	})

	t.Run("GetFile_NotFound", func(t *testing.T) {
		_, err := f.client.GetFile(ctx, &hivemindv1.GetFileRequest{FileId: 999_999})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("GetFile (nonexistent fileID): got code %v, want %v", status.Code(err), codes.NotFound)
		}
	})

	t.Run("ReadPartial", func(t *testing.T) {
		resp, err := f.client.ReadPartial(ctx, &hivemindv1.ReadPartialRequest{FileId: f.alphaID})
		if err != nil {
			t.Fatalf("ReadPartial: %v", err)
		}
		if len(resp.GetHeaders()) == 0 {
			t.Fatalf("ReadPartial: got 0 headers, want at least 1 (alphaID content has # and ## headers)")
		}
		headerTexts := make([]string, len(resp.GetHeaders()))
		for i, h := range resp.GetHeaders() {
			headerTexts[i] = h.GetHeader()
		}
		found := map[string]bool{}
		for _, h := range headerTexts {
			found[h] = true
		}
		if !found["# Alpha"] {
			t.Fatalf("ReadPartial: headers %v missing expected \"# Alpha\"", headerTexts)
		}
		if !found["## Alpha Details"] {
			t.Fatalf("ReadPartial: headers %v missing expected \"## Alpha Details\"", headerTexts)
		}
	})

	t.Run("GraphNeighbors", func(t *testing.T) {
		resp, err := f.client.GraphNeighbors(ctx, &hivemindv1.GraphNeighborsRequest{
			FileId:         f.alphaID,
			Depth:          1,
			EdgeTypeFilter: hivemindv1.EdgeType_EDGE_TYPE_UNSPECIFIED,
			MaxNodes:       10,
		})
		if err != nil {
			t.Fatalf("GraphNeighbors: %v", err)
		}
		if len(resp.GetNeighbors()) != 1 {
			t.Fatalf("GraphNeighbors: got %d neighbors, want 1", len(resp.GetNeighbors()))
		}
		n := resp.GetNeighbors()[0]
		if n.GetTargetFileId() != f.betaID {
			t.Fatalf("GraphNeighbors: TargetFileId = %d, want %d", n.GetTargetFileId(), f.betaID)
		}
		if n.GetType() != hivemindv1.EdgeType_ENTITY_COOCCUR {
			t.Fatalf("GraphNeighbors: Type = %v, want %v", n.GetType(), hivemindv1.EdgeType_ENTITY_COOCCUR)
		}
		if n.GetWeight() != 7 {
			t.Fatalf("GraphNeighbors: Weight = %d, want 7", n.GetWeight())
		}
	})

	t.Run("SearchCandidates", func(t *testing.T) {
		resp, err := f.client.SearchCandidates(ctx, &hivemindv1.SearchCandidatesRequest{
			Query:      "topics/",
			MaxResults: 0,
		})
		if err != nil {
			t.Fatalf("SearchCandidates: %v", err)
		}

		// Cross-check directly against btree.PrefixScan on the same real store/root.
		direct, err := btree.PrefixScan(f.btreeStore, f.btreeRoot, "topics/")
		if err != nil {
			t.Fatalf("btree.PrefixScan (cross-check): %v", err)
		}
		if len(resp.GetCandidates()) != len(direct) {
			t.Fatalf("SearchCandidates: got %d candidates, want %d (from direct PrefixScan)", len(resp.GetCandidates()), len(direct))
		}

		gotPaths := make([]string, len(resp.GetCandidates()))
		for i, c := range resp.GetCandidates() {
			gotPaths[i] = c.GetPath()
		}
		wantPaths := make([]string, len(direct))
		for i, e := range direct {
			wantPaths[i] = e.Path
		}
		sort.Strings(gotPaths)
		sort.Strings(wantPaths)
		for i := range wantPaths {
			if gotPaths[i] != wantPaths[i] {
				t.Fatalf("SearchCandidates: paths = %v, want %v", gotPaths, wantPaths)
			}
		}
	})

	t.Run("ProposeSplit_Unimplemented", func(t *testing.T) {
		proposer := split.NewGRPCSplitProposer(f.client, 5*time.Second)
		_, err := proposer.ProposeSplit([]byte("# Some File\n\nbig content\n"))
		if err == nil {
			t.Fatal("ProposeSplit: got nil error, want a wrapped codes.Unimplemented error (server-side ProposeSplit is intentionally unimplemented -- see docs/LLD/rpc.md and issue #18)")
		}
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("ProposeSplit: error %v does not carry a gRPC status (status.FromError ok=false); GRPCSplitProposer's %%w wrapping must preserve gRPC-status introspectability", err)
		}
		if st.Code() != codes.Unimplemented {
			t.Fatalf("ProposeSplit: got code %v, want %v", st.Code(), codes.Unimplemented)
		}
	})

	// Interceptor assertion, deliberately last: confirms LatencyInterceptor genuinely fired
	// for real RPCs made above (proving the grpc.UnaryInterceptor wiring is real, not that
	// RPCs merely happened to succeed without it).
	t.Run("LatencyInterceptor_Wired", func(t *testing.T) {
		metrics := f.recorder.snapshot()
		if len(metrics) == 0 {
			t.Fatal("LatencyInterceptor_Wired: recorder captured 0 RPCMetric records; interceptor did not fire for any call")
		}

		seenMethods := map[string]bool{}
		for _, m := range metrics {
			seenMethods[m.Method] = true
			if m.Duration < 0 {
				t.Fatalf("LatencyInterceptor_Wired: metric for %s has negative Duration %v", m.Method, m.Duration)
			}
		}

		wantMethods := []string{
			"/hivemind.v1.HiveMind/PutSegment",
			"/hivemind.v1.HiveMind/GetFile",
			"/hivemind.v1.HiveMind/ReadPartial",
			"/hivemind.v1.HiveMind/GraphNeighbors",
			"/hivemind.v1.HiveMind/SearchCandidates",
			"/hivemind.v1.HiveMind/ProposeSplit",
		}
		for _, wm := range wantMethods {
			if !seenMethods[wm] {
				t.Fatalf("LatencyInterceptor_Wired: no RPCMetric recorded for %s; seen methods: %v", wm, seenMethods)
			}
		}

		// The ProposeSplit call's metric must reflect the Unimplemented status code, not
		// codes.OK -- confirming the interceptor observes the real per-call outcome, not a
		// hardcoded success.
		var sawProposeSplitUnimplemented bool
		for _, m := range metrics {
			if m.Method == "/hivemind.v1.HiveMind/ProposeSplit" && m.Code == codes.Unimplemented {
				sawProposeSplitUnimplemented = true
			}
		}
		if !sawProposeSplitUnimplemented {
			t.Fatal("LatencyInterceptor_Wired: no ProposeSplit RPCMetric recorded with codes.Unimplemented")
		}
	})
}
