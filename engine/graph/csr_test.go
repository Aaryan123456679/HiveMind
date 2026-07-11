package graph

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// neighborsEqual asserts that two CSREdge slices contain the same edges, ignoring order
// (BuildCSR/WriteCSR/LoadCSR preserve insertion order within a node in this implementation,
// but tests should not over-assert on that incidental detail beyond what's needed).
func neighborsEqual(t *testing.T, got, want []CSREdge) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("neighbor count mismatch: got %d, want %d (got=%v, want=%v)", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("neighbor %d mismatch: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestCSRFormat is the test spec required by issue #15 subtask 3.1.1: write adjacency data
// for several fileIDs, reopen graph.dat (simulating a process restart via a fresh LoadCSR
// call, independent of the CSRGraph used to write it), and assert identical adjacency on
// reload.
func TestCSRFormat(t *testing.T) {
	adjacency := map[uint64][]CSREdge{
		1: {
			{Target: 2, Type: EdgeSplitSibling, Weight: 1, LastUpdated: 1000},
			{Target: 3, Type: EdgeRedirect, Weight: 0, LastUpdated: 1001},
		},
		2: {
			{Target: 1, Type: EdgeSplitSibling, Weight: 1, LastUpdated: 1000},
		},
		// Node 5 has an adjacency entry but zero outbound edges (e.g. a leaf/dangling node).
		5: {},
		42: {
			{Target: 1, Type: EdgeRedirect, Weight: 7, LastUpdated: 5000},
			{Target: 2, Type: EdgeRedirect, Weight: 8, LastUpdated: 5001},
			{Target: 3, Type: EdgeSplitSibling, Weight: 9, LastUpdated: 5002},
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "graph.dat")

	built := BuildCSR(adjacency)
	if err := WriteCSR(path, built); err != nil {
		t.Fatalf("WriteCSR: %v", err)
	}

	// Simulate a process restart: load into a brand new CSRGraph value, independent of
	// `built`, from the file on disk only.
	reloaded, err := LoadCSR(path)
	if err != nil {
		t.Fatalf("LoadCSR: %v", err)
	}

	if reloaded.NodeCount() != built.NodeCount() {
		t.Fatalf("NodeCount mismatch after reload: got %d, want %d", reloaded.NodeCount(), built.NodeCount())
	}
	if reloaded.EdgeCount() != built.EdgeCount() {
		t.Fatalf("EdgeCount mismatch after reload: got %d, want %d", reloaded.EdgeCount(), built.EdgeCount())
	}

	for fileID, wantEdges := range adjacency {
		var want []CSREdge
		if len(wantEdges) > 0 {
			want = wantEdges
		}
		neighborsEqual(t, reloaded.Neighbors(fileID), want)
	}

	// A fileID never present in the adjacency map must have no neighbors, both before and
	// after reload.
	if got := reloaded.Neighbors(999); got != nil {
		t.Fatalf("Neighbors(999) on unknown fileID: got %v, want nil", got)
	}
	if got := built.Neighbors(999); got != nil {
		t.Fatalf("Neighbors(999) on unknown fileID (pre-reload): got %v, want nil", got)
	}
}

// TestCSREmptyGraph asserts that a graph with zero nodes and zero edges round-trips
// through WriteCSR/LoadCSR without error.
func TestCSREmptyGraph(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.dat")

	built := BuildCSR(map[uint64][]CSREdge{})
	if err := WriteCSR(path, built); err != nil {
		t.Fatalf("WriteCSR: %v", err)
	}

	reloaded, err := LoadCSR(path)
	if err != nil {
		t.Fatalf("LoadCSR: %v", err)
	}
	if reloaded.NodeCount() != 0 {
		t.Fatalf("NodeCount: got %d, want 0", reloaded.NodeCount())
	}
	if reloaded.EdgeCount() != 0 {
		t.Fatalf("EdgeCount: got %d, want 0", reloaded.EdgeCount())
	}
	if got := reloaded.Neighbors(1); got != nil {
		t.Fatalf("Neighbors(1) on empty graph: got %v, want nil", got)
	}
}

// TestCSRCorruptedPayloadDetected asserts that flipping a byte in the payload region of a
// valid graph.dat file is caught by LoadCSR's CRC32 check, rather than silently producing
// wrong adjacency data.
func TestCSRCorruptedPayloadDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.dat")

	adjacency := map[uint64][]CSREdge{
		1: {{Target: 2, Type: EdgeSplitSibling, Weight: 3, LastUpdated: 42}},
	}
	built := BuildCSR(adjacency)
	if err := WriteCSR(path, built); err != nil {
		t.Fatalf("WriteCSR: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) <= csrHeaderSize {
		t.Fatalf("test file too small to corrupt payload: %d bytes", len(data))
	}
	// Flip a bit in the payload (first byte after the header, part of the nodeIDs array).
	data[csrHeaderSize] ^= 0xFF
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile (corrupting): %v", err)
	}

	if _, err := LoadCSR(path); err == nil {
		t.Fatalf("LoadCSR on corrupted payload: got nil error, want a CRC-mismatch error")
	}
}

// TestCSRTruncatedHeaderRejected asserts that a file shorter than the fixed header size is
// rejected with an error, not a panic or silently-wrong decode.
func TestCSRTruncatedHeaderRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.dat")

	if err := os.WriteFile(path, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := LoadCSR(path); err == nil {
		t.Fatalf("LoadCSR on truncated header: got nil error, want an error")
	}
}

// TestLoadCSRRejectsUnknownEdgeType is the test spec required by issue #49 subtask 4.5.11.3:
// construct a graph.dat fixture whose on-disk EdgeType byte is out of range (simulating a
// second write path into graph.dat, now that PutEdge exists, producing an unrecognized edge
// type), and assert LoadCSR returns an explicit error rather than silently decoding it.
//
// The fixture is built by first writing a normal, valid graph via WriteCSR (so the header,
// offsets, and CRC are all produced by the real code path), then patching the first edge's
// on-disk Type byte to a value ValidEdgeType (edge.go) rejects and recomputing the payload CRC32
// to match - this isolates the failure to decodeCSREdge's type-validation guard specifically,
// distinct from TestCSRCorruptedPayloadDetected's CRC-mismatch case above. WriteCSR itself
// already refuses to persist an invalid EdgeType (see its own ValidEdgeType check), so this
// fixture must be hand-patched to exercise LoadCSR's defensive decode-time guard, matching
// edge_append.go's decodeEdge convention of explicit-error-not-silent-decode.
func TestLoadCSRRejectsUnknownEdgeType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.dat")

	adjacency := map[uint64][]CSREdge{
		1: {{Target: 2, Type: EdgeSplitSibling, Weight: 3, LastUpdated: 42}},
	}
	built := BuildCSR(adjacency)
	if err := WriteCSR(path, built); err != nil {
		t.Fatalf("WriteCSR: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	nodeCount := uint64(built.NodeCount())
	edgesStart := csrHeaderSize + nodeCount*8 + (nodeCount+1)*8
	typeOffset := edgesStart + offCSREdgeType
	if typeOffset >= uint64(len(data)) {
		t.Fatalf("computed type byte offset %d out of range for %d-byte file", typeOffset, len(data))
	}

	const outOfRangeEdgeType = 99 // not EdgeTypeInvalid(0) nor any of the 4 ValidEdgeType values
	if ValidEdgeType(EdgeType(outOfRangeEdgeType)) {
		t.Fatalf("test fixture bug: chosen sentinel %d is unexpectedly a valid EdgeType", outOfRangeEdgeType)
	}
	data[typeOffset] = outOfRangeEdgeType

	// Recompute the payload CRC32 so the failure below is attributable to the EdgeType
	// validation guard, not the already-covered CRC-mismatch path.
	payload := data[csrHeaderSize:]
	binary.LittleEndian.PutUint32(data[offCSRPayloadCRC:], crc32.ChecksumIEEE(payload))

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile (patching EdgeType byte): %v", err)
	}

	if _, err := LoadCSR(path); err == nil {
		t.Fatalf("LoadCSR on graph.dat with out-of-range EdgeType byte: got nil error, want an explicit error")
	}
}

// TestCSRLargeAdjacency exercises the offsets-array math at non-trivial scale: many nodes,
// many edges per node, asserting full round-trip correctness.
func TestCSRLargeAdjacency(t *testing.T) {
	const numNodes = 500
	const edgesPerNode = 20

	adjacency := make(map[uint64][]CSREdge, numNodes)
	for n := uint64(0); n < numNodes; n++ {
		edges := make([]CSREdge, 0, edgesPerNode)
		for e := 0; e < edgesPerNode; e++ {
			edges = append(edges, CSREdge{
				Target:      (n + uint64(e) + 1) % numNodes,
				Type:        EdgeSplitSibling,
				Weight:      uint32(n*100 + uint64(e)),
				LastUpdated: int64(n*1000 + uint64(e)),
			})
		}
		adjacency[n] = edges
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "graph.dat")

	built := BuildCSR(adjacency)
	if err := WriteCSR(path, built); err != nil {
		t.Fatalf("WriteCSR: %v", err)
	}

	reloaded, err := LoadCSR(path)
	if err != nil {
		t.Fatalf("LoadCSR: %v", err)
	}

	if reloaded.NodeCount() != numNodes {
		t.Fatalf("NodeCount: got %d, want %d", reloaded.NodeCount(), numNodes)
	}
	if reloaded.EdgeCount() != numNodes*edgesPerNode {
		t.Fatalf("EdgeCount: got %d, want %d", reloaded.EdgeCount(), numNodes*edgesPerNode)
	}

	for n := uint64(0); n < numNodes; n++ {
		got := reloaded.Neighbors(n)
		want := adjacency[n]
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Neighbors(%d) mismatch:\ngot  %+v\nwant %+v", n, got, want)
		}
	}
}
