// Command smokeserver boots a real HiveMind gRPC server (engine/rpc.Server), backed by
// real (not mocked/faked) engine/catalog, engine/graph, and engine/btree storage rooted
// at a fresh directory, bound to a real OS-level net.Listener on 127.0.0.1 (an
// OS-assigned ephemeral port unless -addr overrides it).
//
// Built specifically for issue #19 subtask 3.5.2's end-to-end ingestion smoke run: unlike
// engine/rpc/integration_test.go (which starts an equivalent real server but only from
// within a Go test, in-process), this binary is a real standalone subprocess a
// non-Go caller (the Python ingestion pipeline, via
// agents/ingestion/test_e2e_smoke.py) can launch, read a listen address from, drive real
// RPCs against, and then terminate -- exercising the actual wire protocol end-to-end
// rather than any in-process mock. It intentionally seeds no fixture content (unlike the
// integration test's fixture): the smoke run itself is responsible for populating state
// via real PutSegment/PutEdge/PutEntity calls, mirroring a fresh engine instance.
//
// Usage: smokeserver -root <dir> [-addr 127.0.0.1:0]
// On success, prints exactly one line to stdout: "LISTENING <host:port>", then blocks
// serving RPCs until it receives SIGINT/SIGTERM, at which point it gracefully stops the
// gRPC server and exits 0.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/graph"
	"github.com/Aaryan123456679/HiveMind/engine/rpc"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
	"github.com/Aaryan123456679/HiveMind/engine/split"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// bootstrapPath is the placeholder path/fileID pair seeded into the topics B+Tree at
// startup, purely to obtain a valid (non-reserved) rootNodeID -- see run()'s comment at
// the btree.Insert call site.
const bootstrapPath = "_smokeserver/bootstrap"

func main() {
	root := flag.String("root", "", "root directory for real catalog/content/btree/graph storage (must exist)")
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on (default: OS-assigned ephemeral loopback port)")
	flag.Parse()

	if *root == "" {
		log.Fatal("smokeserver: -root is required")
	}

	if err := run(*root, *addr); err != nil {
		log.Fatalf("smokeserver: %v", err)
	}
}

func run(root, addr string) error {
	fm, err := catalog.Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		return fmt.Errorf("catalog.Open: %w", err)
	}
	defer fm.Close()

	cat := catalog.NewCatalog(fm)

	idAlloc, err := catalog.NewIDAllocator(fm)
	if err != nil {
		return fmt.Errorf("catalog.NewIDAllocator: %w", err)
	}
	defer idAlloc.Close()

	w, err := wal.OpenWriter(filepath.Join(root, "content.wal"), 1<<20)
	if err != nil {
		return fmt.Errorf("wal.OpenWriter: %w", err)
	}
	defer w.Close()

	cs, err := catalog.OpenContentStore(root, cat, w)
	if err != nil {
		return fmt.Errorf("catalog.OpenContentStore: %w", err)
	}

	// Subtask 4.5.3.1 (issue #40): wire a real engine/split.Trigger into the
	// ContentStore's Append path, via SetSplitTrigger, so that a size-threshold
	// crossing on a real PutSegment append actually surfaces a split signal in this
	// production server binary -- not just in engine/split/trigger_test.go's unit
	// coverage of Trigger.Detect in isolation. engine/catalog cannot import
	// engine/split directly (engine/split already imports engine/catalog, so the
	// reverse import would be circular -- see content.go's SplitTriggerFunc doc
	// comment); this main.go composition root is where the two are wired together.
	splitTrigger := split.DefaultTrigger()
	cs.SetSplitTrigger(func(fileID, oldSizeBytes, newSizeBytes uint64) bool {
		sig, crossed := splitTrigger.Detect(fileID, oldSizeBytes, newSizeBytes)
		if crossed {
			log.Printf("smokeserver: split-eligibility signal: fileID=%d oldSizeBytes=%d newSizeBytes=%d thresholdBytes=%d", sig.FileID, sig.OldSizeBytes, sig.NewSizeBytes, sig.ThresholdBytes)
		}
		return crossed
	})

	idxFile, err := btree.OpenIndexFile(filepath.Join(root, "topics.idx"))
	if err != nil {
		return fmt.Errorf("btree.OpenIndexFile: %w", err)
	}
	defer idxFile.Close()
	store := btree.NewNodeStore(idxFile)

	nodeAlloc, err := btree.NewNodeAllocator(store)
	if err != nil {
		return fmt.Errorf("btree.NewNodeAllocator: %w", err)
	}
	defer nodeAlloc.Close()

	// btree.PrefixScan/Lookup deliberately do not special-case rootNodeID == the
	// reserved sentinel (0) -- a tree that has never had anything inserted into it (see
	// engine/btree/scan.go's PrefixScan doc comment: "out of scope here too"). A
	// completely fresh engine instance (this binary's whole purpose) would otherwise
	// make every SearchCandidates call error instead of returning an empty result, so
	// this seeds exactly one bootstrap placeholder entry (fileID
	// catalog.InvalidFileID/0, a value PutSegment/the IDAllocator will never actually
	// allocate) directly via btree.Insert -- not through any RPC -- purely to obtain a
	// valid, non-reserved rootNodeID. This is a workaround for a distinct, pre-existing,
	// deliberately-out-of-scope btree limitation, NOT an attempt to fix or paper over
	// F4 (PutSegment's CREATE path still never inserts into this tree either way).
	rootNodeID, err := btree.Insert(store, nodeAlloc, 0, bootstrapPath, catalog.InvalidFileID)
	if err != nil {
		return fmt.Errorf("btree.Insert (bootstrap root): %w", err)
	}
	// pathIndex wraps the same store/nodeAlloc/rootNodeID as a self-tracking *btree.Tree
	// (issue #43, commit 2/3): PutSegment's CREATE handler now inserts newly created
	// files' paths into this tree via pathIndex.Insert, and SearchCandidates reads via
	// pathIndex.Store/pathIndex.Root() -- both against the same underlying tree this
	// bootstrap placeholder was seeded into above.
	pathIndex := btree.NewTree(store, nodeAlloc, rootNodeID)

	// Empty CSR graph: this is a fresh engine instance, no pre-existing edges. PutEdge's
	// edgeLog below is the real write path; Compact would fold it into a fresh CSR
	// snapshot, but GraphNeighbors reads are not part of this smoke run's scope.
	g := graph.BuildCSR(map[uint64][]graph.CSREdge{})

	edgeLog, err := graph.OpenEdgeLog(filepath.Join(root, "graph.dat"))
	if err != nil {
		return fmt.Errorf("graph.OpenEdgeLog: %w", err)
	}
	defer edgeLog.Close()

	entityIdxFile, err := btree.OpenIndexFile(filepath.Join(root, "entity.idx"))
	if err != nil {
		return fmt.Errorf("btree.OpenIndexFile(entity.idx): %w", err)
	}
	defer entityIdxFile.Close()
	entityStore := btree.NewNodeStore(entityIdxFile)

	entityAlloc, err := btree.NewNodeAllocator(entityStore)
	if err != nil {
		return fmt.Errorf("btree.NewNodeAllocator(entity): %w", err)
	}
	defer entityAlloc.Close()
	entityIndex := btree.NewTree(entityStore, entityAlloc, 0)

	srv, err := rpc.NewServer(cat, cs, idAlloc, g, pathIndex, edgeLog, entityIndex)
	if err != nil {
		return fmt.Errorf("rpc.NewServer: %w", err)
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("net.Listen: %w", err)
	}

	gsrv := grpc.NewServer()
	hivemindv1.RegisterHiveMindServer(gsrv, srv)

	// Exactly one stdout line, machine-parseable by the Python smoke-run launcher.
	fmt.Printf("LISTENING %s\n", lis.Addr().String())
	os.Stdout.Sync()

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- gsrv.Serve(lis)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("smokeserver: received %s, shutting down", sig)
		gsrv.GracefulStop()
		<-serveErrCh
		return nil
	case err := <-serveErrCh:
		if err != nil {
			return fmt.Errorf("grpc Serve: %w", err)
		}
		return nil
	}
}
