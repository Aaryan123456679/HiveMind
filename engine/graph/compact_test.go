package graph

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// sortedNeighbors returns a copy of edges sorted by (Target, Type), so tests
// can compare merge results without depending on mergeEdges' incidental
// (map-iteration-derived) output order.
func sortedNeighbors(edges []CSREdge) []CSREdge {
	out := make([]CSREdge, len(edges))
	copy(out, edges)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func assertNeighbors(t *testing.T, got, want []CSREdge) {
	t.Helper()
	gotSorted, wantSorted := sortedNeighbors(got), sortedNeighbors(want)
	if len(gotSorted) != len(wantSorted) {
		t.Fatalf("neighbor count mismatch: got %d (%+v), want %d (%+v)", len(gotSorted), gotSorted, len(wantSorted), wantSorted)
	}
	for i := range wantSorted {
		if gotSorted[i] != wantSorted[i] {
			t.Fatalf("neighbor %d mismatch: got %+v, want %+v", i, gotSorted[i], wantSorted[i])
		}
	}
}

// TestCompaction is the test spec required by issue #15 subtask 3.1.3: append
// many edges, including repeated ENTITY_COOCCUR edges, via the edge log, run
// compaction, and assert the resulting CSR is correctly merged/weighted.
func TestCompaction(t *testing.T) {
	t.Run("EntityCooccurWeightsSum", func(t *testing.T) {
		dir := t.TempDir()
		graphPath := filepath.Join(dir, "graph.dat")
		logRoot := filepath.Join(dir, "edgelogs")

		l, err := OpenEdgeLog(logRoot)
		if err != nil {
			t.Fatalf("OpenEdgeLog: %v", err)
		}
		defer l.Close()

		const source, target = 10, 20
		const occurrences = 5
		for i := 0; i < occurrences; i++ {
			e := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 1, LastUpdated: int64(1000 + i)}
			if err := l.AppendEdge(source, e); err != nil {
				t.Fatalf("AppendEdge #%d: %v", i, err)
			}
		}

		newGraph, err := Compact(graphPath, l)
		if err != nil {
			t.Fatalf("Compact: %v", err)
		}

		got := newGraph.Neighbors(source)
		want := []CSREdge{{Target: target, Type: EdgeEntityCooccur, Weight: occurrences, LastUpdated: 1000 + occurrences - 1}}
		assertNeighbors(t, got, want)

		// Reload from disk (not just the in-memory return value) to confirm
		// WriteCSR actually persisted the merged/weighted result.
		reloaded, err := LoadCSR(graphPath)
		if err != nil {
			t.Fatalf("LoadCSR: %v", err)
		}
		assertNeighbors(t, reloaded.Neighbors(source), want)
	})

	t.Run("NonCooccurDedupLastWrite", func(t *testing.T) {
		dir := t.TempDir()
		graphPath := filepath.Join(dir, "graph.dat")
		logRoot := filepath.Join(dir, "edgelogs")

		l, err := OpenEdgeLog(logRoot)
		if err != nil {
			t.Fatalf("OpenEdgeLog: %v", err)
		}
		defer l.Close()

		const source, target = 1, 2
		older := CSREdge{Target: target, Type: EdgeSplitSibling, Weight: 1, LastUpdated: 100}
		newer := CSREdge{Target: target, Type: EdgeSplitSibling, Weight: 1, LastUpdated: 200}
		if err := l.AppendEdge(source, older); err != nil {
			t.Fatalf("AppendEdge older: %v", err)
		}
		if err := l.AppendEdge(source, newer); err != nil {
			t.Fatalf("AppendEdge newer: %v", err)
		}

		newGraph, err := Compact(graphPath, l)
		if err != nil {
			t.Fatalf("Compact: %v", err)
		}

		got := newGraph.Neighbors(source)
		// Exactly one entry (no duplication), with the later occurrence's
		// values (no weight summing for non-ENTITY_COOCCUR types).
		assertNeighbors(t, got, []CSREdge{newer})
	})

	t.Run("MergesWithExistingGraph", func(t *testing.T) {
		dir := t.TempDir()
		graphPath := filepath.Join(dir, "graph.dat")
		logRoot := filepath.Join(dir, "edgelogs")

		const source, cooccurTarget, otherTarget = 1, 2, 3
		initial := BuildCSR(map[uint64][]CSREdge{
			source: {
				{Target: cooccurTarget, Type: EdgeEntityCooccur, Weight: 3, LastUpdated: 500},
				{Target: otherTarget, Type: EdgeSplitSibling, Weight: 1, LastUpdated: 501},
			},
		})
		if err := WriteCSR(graphPath, initial); err != nil {
			t.Fatalf("WriteCSR (seed): %v", err)
		}

		l, err := OpenEdgeLog(logRoot)
		if err != nil {
			t.Fatalf("OpenEdgeLog: %v", err)
		}
		defer l.Close()

		// One more co-occurrence observation for the edge already in
		// graph.dat: weight should continue accumulating from 3, not reset.
		if err := l.AppendEdge(source, CSREdge{Target: cooccurTarget, Type: EdgeEntityCooccur, Weight: 2, LastUpdated: 600}); err != nil {
			t.Fatalf("AppendEdge: %v", err)
		}

		newGraph, err := Compact(graphPath, l)
		if err != nil {
			t.Fatalf("Compact: %v", err)
		}

		want := []CSREdge{
			{Target: cooccurTarget, Type: EdgeEntityCooccur, Weight: 5, LastUpdated: 600},
			{Target: otherTarget, Type: EdgeSplitSibling, Weight: 1, LastUpdated: 501},
		}
		assertNeighbors(t, newGraph.Neighbors(source), want)
	})

	t.Run("NoEdgeLogIsNoop", func(t *testing.T) {
		dir := t.TempDir()
		graphPath := filepath.Join(dir, "graph.dat")
		logRoot := filepath.Join(dir, "edgelogs")

		initial := BuildCSR(map[uint64][]CSREdge{
			1: {{Target: 2, Type: EdgeRedirect, Weight: 0, LastUpdated: 42}},
		})
		if err := WriteCSR(graphPath, initial); err != nil {
			t.Fatalf("WriteCSR (seed): %v", err)
		}
		before, err := os.ReadFile(graphPath)
		if err != nil {
			t.Fatalf("ReadFile before: %v", err)
		}

		l, err := OpenEdgeLog(logRoot)
		if err != nil {
			t.Fatalf("OpenEdgeLog: %v", err)
		}
		defer l.Close()

		if _, err := Compact(graphPath, l); err != nil {
			t.Fatalf("Compact: %v", err)
		}

		after, err := os.ReadFile(graphPath)
		if err != nil {
			t.Fatalf("ReadFile after: %v", err)
		}
		if string(before) != string(after) {
			t.Fatalf("graph.dat changed despite empty edge log:\nbefore=%x\nafter=%x", before, after)
		}
	})

	t.Run("TruncatesLogsAfterCompaction", func(t *testing.T) {
		dir := t.TempDir()
		graphPath := filepath.Join(dir, "graph.dat")
		logRoot := filepath.Join(dir, "edgelogs")

		l, err := OpenEdgeLog(logRoot)
		if err != nil {
			t.Fatalf("OpenEdgeLog: %v", err)
		}
		defer l.Close()

		const source = 7
		if err := l.AppendEdge(source, CSREdge{Target: 8, Type: EdgeEntityCooccur, Weight: 1, LastUpdated: 1}); err != nil {
			t.Fatalf("AppendEdge: %v", err)
		}

		if _, err := Compact(graphPath, l); err != nil {
			t.Fatalf("Compact: %v", err)
		}

		got, err := l.ReadNode(source)
		if err != nil {
			t.Fatalf("ReadNode after compaction: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected node %d's edge log to be truncated after compaction, got %d entries: %+v", source, len(got), got)
		}

		// A fresh AppendEdge after truncation must still work (log reopens
		// cleanly rather than erroring or resurrecting stale state).
		if err := l.AppendEdge(source, CSREdge{Target: 9, Type: EdgeEntityCooccur, Weight: 1, LastUpdated: 2}); err != nil {
			t.Fatalf("AppendEdge after truncate: %v", err)
		}
		got2, err := l.ReadNode(source)
		if err != nil {
			t.Fatalf("ReadNode after post-truncate append: %v", err)
		}
		assertNeighbors(t, got2, []CSREdge{{Target: 9, Type: EdgeEntityCooccur, Weight: 1, LastUpdated: 2}})
	})
}

// TestCompaction_CrashBeforeRenameLeavesOldGraphAndLogsIntact is this
// subtask's crash-injection test (matching engine/wal's own established
// precedent for exercising crash windows directly rather than only asserting
// the happy path). It does not spawn a real subprocess crash (unlike
// engine/wal/crash_subprocess_test.go, which tests torn-write recovery at the
// byte level); instead it simulates "the process died before WriteCSR's
// rename completed" by making the graph.dat directory unwritable so WriteCSR
// itself fails deterministically before ever reaching its rename step, and
// then asserts the documented invariant: no graph.dat was created/modified,
// and the edge log is left completely untouched (safe to retry).
func TestCompaction_CrashBeforeRenameLeavesOldGraphAndLogsIntact(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: directory permission bits do not block writes, cannot simulate the crash window this test needs")
	}

	dir := t.TempDir()
	graphDir := filepath.Join(dir, "graphdir")
	if err := os.MkdirAll(graphDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	graphPath := filepath.Join(graphDir, "graph.dat")
	logRoot := filepath.Join(dir, "edgelogs")

	l, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	const source = 1
	edge := CSREdge{Target: 2, Type: EdgeEntityCooccur, Weight: 1, LastUpdated: 1}
	if err := l.AppendEdge(source, edge); err != nil {
		t.Fatalf("AppendEdge: %v", err)
	}

	// Make graphDir read-only so WriteCSR's os.CreateTemp (which must create a
	// new temp file inside it) fails deterministically before ever reaching
	// its fsync/rename steps - the same observable outcome as a process crash
	// in that window: no new graph.dat, temp file, or truncated edge log.
	if err := os.Chmod(graphDir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(graphDir, 0o755)

	_, err = Compact(graphPath, l)
	if err == nil {
		t.Fatalf("expected Compact to fail while graphDir is read-only, got nil error")
	}

	if _, statErr := os.Stat(graphPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no graph.dat to have been created, got stat err=%v", statErr)
	}

	os.Chmod(graphDir, 0o755)

	got, err := l.ReadNode(source)
	if err != nil {
		t.Fatalf("ReadNode: %v", err)
	}
	assertNeighbors(t, got, []CSREdge{edge})
}

// TestCompaction_TruncateFailureDoesNotLoseGraphUpdate proves the second half
// of this subtask's crash-safety ordering: once WriteCSR's rename has
// succeeded, graph.dat is durably correct independent of whether the
// subsequent per-node log truncation succeeds. It forces TruncateNode to fail
// for one node (by making that node's log directory read-only) after a
// successful Compact write, and asserts the merged graph.dat is still
// present and correct on disk despite Compact returning a non-nil error.
func TestCompaction_TruncateFailureDoesNotLoseGraphUpdate(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: directory permission bits do not block writes, cannot simulate the truncate-failure window this test needs")
	}

	dir := t.TempDir()
	graphPath := filepath.Join(dir, "graph.dat")
	logRoot := filepath.Join(dir, "edgelogs")

	l, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	const source, target = 5, 6
	edge := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 3, LastUpdated: 42}
	if err := l.AppendEdge(source, edge); err != nil {
		t.Fatalf("AppendEdge: %v", err)
	}

	// Ensure the writer is closed (and dropped from the cache) before we lock
	// down the directory, so TruncateNode's own os.Remove calls - not
	// wal.Writer's already-open file descriptor - are what fails.
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	l2, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("re-OpenEdgeLog: %v", err)
	}
	defer l2.Close()

	nodeDir := l2.nodeDir(source)
	// Lock down the node's log directory itself so os.Remove on the segment
	// files inside it fails (removing a file requires write permission on its
	// parent directory, not the file itself).
	if err := os.Chmod(nodeDir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(nodeDir, 0o755)

	_, err = Compact(graphPath, l2)
	if err == nil {
		t.Fatalf("expected Compact to report a truncate-phase error, got nil")
	}

	os.Chmod(nodeDir, 0o755)

	reloaded, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR: %v (graph.dat should be durable even though truncation failed)", err)
	}
	assertNeighbors(t, reloaded.Neighbors(source), []CSREdge{edge})
}

// TestCompaction_RetryAfterTruncateFailureDoesNotDoubleCountWeight is this
// fix's regression test for the corruption bug found in verification of the
// original 3.1.3 implementation: a second Compact call - the documented,
// natural recovery action after a truncate-phase failure - used to re-read
// the still-un-truncated edge-log entry as "incoming" and merge it AGAIN
// against an "existing" graph.dat that (from the first, already-durable
// Compact call) already reflected that entry's contribution, permanently
// doubling (and, on further retries, continuing to compound) the merged
// EdgeEntityCooccur weight. It reuses
// TestCompaction_TruncateFailureDoesNotLoseGraphUpdate's exact setup (append
// one EdgeEntityCooccur edge with Weight=3, force TruncateNode to fail after
// WriteCSR's rename has already succeeded) and then performs the natural
// retry: a second Compact call once the failure condition is lifted. The
// weight must still be exactly 3 after that retry, not 6.
func TestCompaction_RetryAfterTruncateFailureDoesNotDoubleCountWeight(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: directory permission bits do not block writes, cannot simulate the truncate-failure window this test needs")
	}

	dir := t.TempDir()
	graphPath := filepath.Join(dir, "graph.dat")
	logRoot := filepath.Join(dir, "edgelogs")

	l, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	const source, target = 5, 6
	edge := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 3, LastUpdated: 42}
	if err := l.AppendEdge(source, edge); err != nil {
		t.Fatalf("AppendEdge: %v", err)
	}

	// Ensure the writer is closed (and dropped from the cache) before we lock
	// down the directory, so TruncateNode's own os.Remove calls - not
	// wal.Writer's already-open file descriptor - are what fails.
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	l2, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("re-OpenEdgeLog: %v", err)
	}
	defer l2.Close()

	nodeDir := l2.nodeDir(source)
	if err := os.Chmod(nodeDir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	// First Compact: WriteCSR's rename succeeds (graph.dat correctly shows
	// Weight=3), but TruncateNode fails because nodeDir is read-only.
	if _, err := Compact(graphPath, l2); err == nil {
		t.Fatalf("expected first Compact to report a truncate-phase error, got nil")
	}

	reloaded, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after first Compact: %v", err)
	}
	assertNeighbors(t, reloaded.Neighbors(source), []CSREdge{edge})

	// Lift the failure condition and retry - the documented recovery action
	// ("safe to simply retry Compact later"). The still-un-truncated log
	// entry from the first run must NOT be re-summed into graph.dat now that
	// it can be read again.
	if err := os.Chmod(nodeDir, 0o755); err != nil {
		t.Fatalf("Chmod restore: %v", err)
	}

	if _, err := Compact(graphPath, l2); err != nil {
		t.Fatalf("second (retry) Compact: unexpected error: %v", err)
	}

	reloaded2, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after retry Compact: %v", err)
	}
	assertNeighbors(t, reloaded2.Neighbors(source), []CSREdge{edge})

	// The retry should also have finished the truncation this time, leaving
	// no residual edge-log entries for the node.
	got, err := l2.ReadNode(source)
	if err != nil {
		t.Fatalf("ReadNode after retry: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected edge log to be empty after successful retry truncation, got %+v", got)
	}
}

// TestTruncateNode (belongs conceptually with edgelog_test.go's suite, kept
// here since it's exercised primarily as compaction's own post-write step)
// confirms EdgeLog.TruncateNode in isolation: it resets a node's log to
// empty, works whether or not the node currently has an open writer, and is
// a no-op (not an error) for a fileID with no log at all.
func TestTruncateNode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "edgelogs")
	l, err := OpenEdgeLog(root)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	// No-op on a fileID with no log yet.
	if err := l.TruncateNode(999); err != nil {
		t.Fatalf("TruncateNode on nonexistent node: %v", err)
	}

	const source = 1
	if err := l.AppendEdge(source, CSREdge{Target: 2, Type: EdgeRedirect, Weight: 0, LastUpdated: 1}); err != nil {
		t.Fatalf("AppendEdge: %v", err)
	}
	if err := l.TruncateNode(source); err != nil {
		t.Fatalf("TruncateNode: %v", err)
	}
	got, err := l.ReadNode(source)
	if err != nil {
		t.Fatalf("ReadNode after truncate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty log after truncate, got %+v", got)
	}

	if err := l.AppendEdge(source, CSREdge{Target: 3, Type: EdgeRedirect, Weight: 0, LastUpdated: 2}); err != nil {
		t.Fatalf("AppendEdge after truncate: %v", err)
	}
	got2, err := l.ReadNode(source)
	if err != nil {
		t.Fatalf("ReadNode after post-truncate append: %v", err)
	}
	assertNeighbors(t, got2, []CSREdge{{Target: 3, Type: EdgeRedirect, Weight: 0, LastUpdated: 2}})
}
