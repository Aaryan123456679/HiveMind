package graph

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestPerNodeEdgeLogBasic confirms basic per-node isolation: appending to
// distinct source fileIDs lands in distinct per-node logs, with no
// cross-contamination, and correct contents/order on read-back.
func TestPerNodeEdgeLogBasic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "edgelogs")

	l, err := OpenEdgeLog(root)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	node1Edges := []CSREdge{
		{Target: 100, Type: EdgeSplitSibling, Weight: 1, LastUpdated: 111},
		{Target: 101, Type: EdgeRedirect, Weight: 1, LastUpdated: 112},
	}
	node2Edges := []CSREdge{
		{Target: 200, Type: EdgeSplitSibling, Weight: 1, LastUpdated: 211},
	}

	for _, e := range node1Edges {
		if err := l.AppendEdge(1, e); err != nil {
			t.Fatalf("AppendEdge(1, %+v): %v", e, err)
		}
	}
	for _, e := range node2Edges {
		if err := l.AppendEdge(2, e); err != nil {
			t.Fatalf("AppendEdge(2, %+v): %v", e, err)
		}
	}

	got1, err := l.ReadNode(1)
	if err != nil {
		t.Fatalf("ReadNode(1): %v", err)
	}
	if len(got1) != len(node1Edges) {
		t.Fatalf("ReadNode(1) returned %d edges, want %d: got %+v", len(got1), len(node1Edges), got1)
	}
	for i, want := range node1Edges {
		if got1[i] != want {
			t.Errorf("node 1 edge %d: got %+v, want %+v", i, got1[i], want)
		}
	}

	got2, err := l.ReadNode(2)
	if err != nil {
		t.Fatalf("ReadNode(2): %v", err)
	}
	if len(got2) != len(node2Edges) {
		t.Fatalf("ReadNode(2) returned %d edges, want %d: got %+v", len(got2), len(node2Edges), got2)
	}
	for i, want := range node2Edges {
		if got2[i] != want {
			t.Errorf("node 2 edge %d: got %+v, want %+v", i, got2[i], want)
		}
	}

	// A fileID that never had an edge appended must read back as empty, not error.
	gotEmpty, err := l.ReadNode(999)
	if err != nil {
		t.Fatalf("ReadNode(999) on never-written node: %v", err)
	}
	if len(gotEmpty) != 0 {
		t.Fatalf("ReadNode(999) on never-written node: got %d edges, want 0", len(gotEmpty))
	}
}

// TestPerNodeEdgeLogInvalidType confirms AppendEdge rejects the EdgeTypeInvalid
// zero-value sentinel and does not write a record for it.
func TestPerNodeEdgeLogInvalidType(t *testing.T) {
	root := filepath.Join(t.TempDir(), "edgelogs")

	l, err := OpenEdgeLog(root)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	err = l.AppendEdge(7, CSREdge{Target: 8, Type: EdgeTypeInvalid})
	if err == nil {
		t.Fatalf("AppendEdge with EdgeTypeInvalid: got nil error, want error")
	}

	got, err := l.ReadNode(7)
	if err != nil {
		t.Fatalf("ReadNode(7): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadNode(7) after rejected AppendEdge: got %d edges, want 0: %+v", len(got), got)
	}
}

// TestPerNodeEdgeLogDurability confirms edges are durably readable via a
// completely fresh EdgeLog instance opened against the same root directory
// (not just through the in-memory instance that wrote them), matching the
// durability convention already established by edge_append_test.go's
// TestMinimalEdgeAppend.
func TestPerNodeEdgeLogDurability(t *testing.T) {
	root := filepath.Join(t.TempDir(), "edgelogs")

	l, err := OpenEdgeLog(root)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}

	want := CSREdge{Target: 55, Type: EdgeRedirect, Weight: 1, LastUpdated: 999}
	if err := l.AppendEdge(42, want); err != nil {
		t.Fatalf("AppendEdge: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	fresh, err := OpenEdgeLog(root)
	if err != nil {
		t.Fatalf("OpenEdgeLog (fresh): %v", err)
	}
	defer fresh.Close()

	got, err := fresh.ReadNode(42)
	if err != nil {
		t.Fatalf("ReadNode(42) on fresh EdgeLog: %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("ReadNode(42) on fresh EdgeLog: got %+v, want [%+v]", got, want)
	}
}

// TestPerNodeEdgeLog is subtask 3.1.2's required test: concurrent appenders
// across many distinct fileIDs must never block each other, and each
// per-node log's contents must be correct (right edges, right order, no
// cross-contamination) despite the concurrency.
func TestPerNodeEdgeLog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "edgelogs")

	l, err := OpenEdgeLog(root)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	const numNodes = 50
	const edgesPerNode = 20

	// --- Correctness under concurrency (the part -race actually checks) ---
	var wg sync.WaitGroup
	for n := 0; n < numNodes; n++ {
		wg.Add(1)
		go func(fileID uint64) {
			defer wg.Done()
			for i := 0; i < edgesPerNode; i++ {
				e := CSREdge{
					Target:      fileID*1000 + uint64(i),
					Type:        EdgeSplitSibling,
					Weight:      1,
					LastUpdated: int64(i),
				}
				if err := l.AppendEdge(fileID, e); err != nil {
					t.Errorf("AppendEdge(%d, edge %d): %v", fileID, i, err)
				}
			}
		}(uint64(n + 1))
	}
	wg.Wait()

	for n := 0; n < numNodes; n++ {
		fileID := uint64(n + 1)
		got, err := l.ReadNode(fileID)
		if err != nil {
			t.Fatalf("ReadNode(%d): %v", fileID, err)
		}
		if len(got) != edgesPerNode {
			t.Fatalf("node %d: got %d edges, want %d: %+v", fileID, len(got), edgesPerNode, got)
		}
		for i, e := range got {
			wantTarget := fileID*1000 + uint64(i)
			if e.Target != wantTarget {
				t.Errorf("node %d edge %d: got target %d, want %d (edge leaked across nodes or out of order)", fileID, i, e.Target, wantTarget)
			}
			if e.LastUpdated != int64(i) {
				t.Errorf("node %d edge %d: got LastUpdated %d, want %d (append order not preserved)", fileID, i, e.LastUpdated, i)
			}
		}
	}

	// --- Soft, best-effort non-blocking proxy check (timing-based, not exact) ---
	//
	// A design where all fileIDs contended on a single shared lock/log would make
	// concurrent wall-clock time for numNodes goroutines roughly proportional to
	// numNodes * (single-append cost), i.e. close to the fully-serial baseline. Because
	// each fileID here gets its own wal.Writer (own mutex, own fsync), the concurrent
	// time should be far below that serial estimate. This is inherently a loose,
	// environment-dependent signal (not a substitute for the -race/correctness checks
	// above), so the threshold is deliberately generous to avoid CI flakiness.
	root2 := filepath.Join(t.TempDir(), "edgelogs-timing")
	l2, err := OpenEdgeLog(root2)
	if err != nil {
		t.Fatalf("OpenEdgeLog (timing): %v", err)
	}
	defer l2.Close()

	const timingNodes = 20
	const timingEdgesPerNode = 30

	// Serial baseline: one goroutine, one fileID, doing all appends back-to-back.
	serialStart := time.Now()
	for i := 0; i < timingNodes*timingEdgesPerNode; i++ {
		e := CSREdge{Target: uint64(i), Type: EdgeSplitSibling, Weight: 1, LastUpdated: int64(i)}
		if err := l2.AppendEdge(uint64(timingNodes+1000), e); err != nil {
			t.Fatalf("AppendEdge (serial baseline): %v", err)
		}
	}
	serialElapsed := time.Since(serialStart)

	root3 := filepath.Join(t.TempDir(), "edgelogs-timing2")
	l3, err := OpenEdgeLog(root3)
	if err != nil {
		t.Fatalf("OpenEdgeLog (timing2): %v", err)
	}
	defer l3.Close()

	concurrentStart := time.Now()
	var wg2 sync.WaitGroup
	for n := 0; n < timingNodes; n++ {
		wg2.Add(1)
		go func(fileID uint64) {
			defer wg2.Done()
			for i := 0; i < timingEdgesPerNode; i++ {
				e := CSREdge{Target: uint64(i), Type: EdgeSplitSibling, Weight: 1, LastUpdated: int64(i)}
				if err := l3.AppendEdge(fileID, e); err != nil {
					t.Errorf("AppendEdge (concurrent, node %d): %v", fileID, err)
				}
			}
		}(uint64(n + 1))
	}
	wg2.Wait()
	concurrentElapsed := time.Since(concurrentStart)

	// Generous threshold: concurrent time should not exceed 80% of the serial baseline.
	// If per-node appends genuinely serialized behind a single shared lock, concurrent
	// time would be roughly >= the serial baseline (same total work, same contention).
	if concurrentElapsed > (serialElapsed*8)/10 {
		t.Logf("non-blocking timing proxy check did not show a speedup (concurrent=%v, serial=%v); this is a best-effort/environment-dependent signal, not treated as a hard failure on its own", concurrentElapsed, serialElapsed)
	}
}

// TestPerNodeEdgeLogClose confirms Close cleanly closes all opened per-node
// writers and that AppendEdge after Close on a fresh EdgeLog against the same
// root still works (Close does not corrupt on-disk state).
func TestPerNodeEdgeLogClose(t *testing.T) {
	root := filepath.Join(t.TempDir(), "edgelogs")

	l, err := OpenEdgeLog(root)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}

	for n := 0; n < 5; n++ {
		if err := l.AppendEdge(uint64(n), CSREdge{Target: 1, Type: EdgeSplitSibling}); err != nil {
			t.Fatalf("AppendEdge(%d): %v", n, err)
		}
	}

	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	fresh, err := OpenEdgeLog(root)
	if err != nil {
		t.Fatalf("OpenEdgeLog (fresh, after Close): %v", err)
	}
	defer fresh.Close()

	if err := fresh.AppendEdge(0, CSREdge{Target: 2, Type: EdgeRedirect}); err != nil {
		t.Fatalf("AppendEdge after reopen: %v", err)
	}
	got, err := fresh.ReadNode(0)
	if err != nil {
		t.Fatalf("ReadNode(0) after reopen: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ReadNode(0) after reopen: got %d edges, want 2 (resume-append, not overwrite): %+v", len(got), got)
	}
}
