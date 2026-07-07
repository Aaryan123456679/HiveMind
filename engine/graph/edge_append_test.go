package graph

import (
	"path/filepath"
	"testing"
)

// TestMinimalEdgeAppend is subtask 2b.3.4's required test: append a few
// edges, confirm they are durably persisted (by reopening/re-reading the
// on-disk log via a fresh ReadAll, not just an in-memory check) and that
// append-only ordering is preserved.
func TestMinimalEdgeAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "graph")

	appender, err := OpenEdgeAppender(dir)
	if err != nil {
		t.Fatalf("OpenEdgeAppender: %v", err)
	}

	wantEdges := []Edge{
		{Source: 43, Target: 44, Type: EdgeSplitSibling},
		{Source: 44, Target: 45, Type: EdgeSplitSibling},
		{Source: 43, Target: 45, Type: EdgeSplitSibling},
		{Source: 10, Target: 43, Type: EdgeRedirect},
	}

	for _, e := range wantEdges {
		if err := appender.AppendEdge(e); err != nil {
			t.Fatalf("AppendEdge(%+v): %v", e, err)
		}
	}

	if err := appender.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Confirm durability by reading back from a completely fresh call
	// against the on-disk directory (no reference to the in-memory
	// appender or its state is used here).
	gotEdges, err := ReadAll(dir)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if len(gotEdges) != len(wantEdges) {
		t.Fatalf("ReadAll returned %d edges, want %d: got %+v", len(gotEdges), len(wantEdges), gotEdges)
	}

	// Confirm append-only ordering: edges must be retrievable in exactly
	// the order they were appended, and must include the source fileID
	// (not just the target), as required by the acceptance criteria.
	for i, want := range wantEdges {
		got := gotEdges[i]
		if got != want {
			t.Fatalf("edge %d: got %+v, want %+v", i, got, want)
		}
		if got.Source != want.Source {
			t.Errorf("edge %d: source fileID not retrievable: got %d, want %d", i, got.Source, want.Source)
		}
	}
}

// TestMinimalEdgeAppendReopenAcrossProcesses confirms that a second
// OpenEdgeAppender against the same directory (simulating a fresh process
// reopening the log) resumes appending after prior edges rather than
// overwriting them, and that ReadAll still observes all edges from both
// "sessions" in append order.
func TestMinimalEdgeAppendReopenAcrossProcesses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "graph")

	first, err := OpenEdgeAppender(dir)
	if err != nil {
		t.Fatalf("OpenEdgeAppender (first): %v", err)
	}
	firstEdge := Edge{Source: 1, Target: 2, Type: EdgeSplitSibling}
	if err := first.AppendEdge(firstEdge); err != nil {
		t.Fatalf("AppendEdge (first): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close (first): %v", err)
	}

	second, err := OpenEdgeAppender(dir)
	if err != nil {
		t.Fatalf("OpenEdgeAppender (second, resumed): %v", err)
	}
	secondEdge := Edge{Source: 2, Target: 3, Type: EdgeRedirect}
	if err := second.AppendEdge(secondEdge); err != nil {
		t.Fatalf("AppendEdge (second): %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close (second): %v", err)
	}

	got, err := ReadAll(dir)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	want := []Edge{firstEdge, secondEdge}
	if len(got) != len(want) {
		t.Fatalf("ReadAll returned %d edges, want %d: got %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("edge %d: got %+v, want %+v (resumed append did not preserve order)", i, got[i], want[i])
		}
	}
}

// TestAppendEdgeRejectsInvalidType confirms AppendEdge fails closed rather
// than silently persisting an edge with the zero-value (invalid) EdgeType.
func TestAppendEdgeRejectsInvalidType(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "graph")

	appender, err := OpenEdgeAppender(dir)
	if err != nil {
		t.Fatalf("OpenEdgeAppender: %v", err)
	}
	defer appender.Close()

	err = appender.AppendEdge(Edge{Source: 1, Target: 2, Type: EdgeTypeInvalid})
	if err == nil {
		t.Fatalf("AppendEdge with EdgeTypeInvalid: expected error, got nil")
	}
}

// TestReadAllOnMissingDir confirms ReadAll on a directory that was never
// opened for appending returns an empty result rather than an error, since
// "no edges yet" is a valid, unexceptional state (not corruption).
func TestReadAllOnMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")

	edges, err := ReadAll(dir)
	if err != nil {
		t.Fatalf("ReadAll on missing dir: unexpected error: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("ReadAll on missing dir: got %d edges, want 0", len(edges))
	}
}
