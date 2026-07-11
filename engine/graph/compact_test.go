package graph

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
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

// TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost is this
// fix cycle's regression test for the F2 finding in
// .cdr/runs/2026-07-08/010-verification/verification.json: a prior fix for
// the retry-idempotency bug (TestCompaction_RetryAfterTruncateFailureDoesNotDoubleCountWeight
// above) made EdgeLog.TruncateNode delete a node's entire log directory once
// its entries were durably folded into graph.dat. Since engine/wal's
// OpenWriter always restarts segment numbering at 0 for a brand-new/empty
// directory, the very next edge appended to that same node after a
// completely ordinary, uneventful truncation would silently reuse a segment
// number compact.go's compact-state sidecar had already recorded as
// "already accounted for" - so the next ordinary Compact run would skip it,
// permanently and silently losing it. No crash or failure injection is
// involved anywhere in this scenario; it is the exact minimal repro from the
// verification finding: append, compact (full success), append again to the
// same node, compact again - both edges must be reflected afterwards.
func TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost(t *testing.T) {
	dir := t.TempDir()
	graphPath := filepath.Join(dir, "graph.dat")
	logRoot := filepath.Join(dir, "edgelogs")

	l, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	const source, target = 100, 200

	// First edge, first (fully successful, no failure injected) compaction.
	first := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 3, LastUpdated: 10}
	if err := l.AppendEdge(source, first); err != nil {
		t.Fatalf("AppendEdge #1: %v", err)
	}
	if _, err := Compact(graphPath, l); err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	reloaded1, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after first Compact: %v", err)
	}
	assertNeighbors(t, reloaded1.Neighbors(source), []CSREdge{first})

	// Second edge to the SAME node, appended only after the first
	// compaction has already fully succeeded (including truncation) - the
	// ordinary, happy-path sequence a real ingestion pipeline would follow.
	second := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 5, LastUpdated: 20}
	if err := l.AppendEdge(source, second); err != nil {
		t.Fatalf("AppendEdge #2: %v", err)
	}

	// Second, ordinary periodic-compaction cycle - nothing unusual injected.
	if _, err := Compact(graphPath, l); err != nil {
		t.Fatalf("second Compact: %v", err)
	}

	reloaded2, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after second Compact: %v", err)
	}
	want := []CSREdge{{Target: target, Type: EdgeEntityCooccur, Weight: 8, LastUpdated: 20}}
	assertNeighbors(t, reloaded2.Neighbors(source), want)

	// A third round-trip (append, compact) confirms the fix is not merely
	// "works once": segment numbering must keep advancing indefinitely,
	// never colliding with a previously-recorded compact-state entry again.
	third := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 1, LastUpdated: 30}
	if err := l.AppendEdge(source, third); err != nil {
		t.Fatalf("AppendEdge #3: %v", err)
	}
	if _, err := Compact(graphPath, l); err != nil {
		t.Fatalf("third Compact: %v", err)
	}
	reloaded3, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after third Compact: %v", err)
	}
	want3 := []CSREdge{{Target: target, Type: EdgeEntityCooccur, Weight: 9, LastUpdated: 30}}
	assertNeighbors(t, reloaded3.Neighbors(source), want3)
}

// TestCompactNormalTruncateThenAppendThenCompactAgain is the exact-named
// regression test required by issue #49's subtask 4.5.11.1 test spec
// (`go test ./engine/graph/... -run
// TestCompactNormalTruncateThenAppendThenCompactAgain`), pinned to the issue's
// own worked repro: append an edge, run a fully successful (no
// failure-injected) Compact, append a second edge to the SAME node, and
// Compact again. Before the fix landed in commit ed57468 (see
// EdgeLog.TruncateNode's doc comment, "Segment numbering must never be
// reused after a successful truncation"), TruncateNode fully removed the
// node's per-node edge-log directory after the first Compact, so
// wal.OpenWriter's next AppendEdge for that node restarted segment
// numbering at 0 - the exact segment number compact.go's compact-state
// sidecar had already durably recorded as "already folded into graph.dat"
// for this node. The second Compact then silently skipped that reused
// segment as already-accounted-for, permanently discarding the second edge
// with no crash or failure injection anywhere in the sequence: LoadCSR
// would show Weight=3 (only the first edge), not 3+5=8, and the edge log
// would be empty too - the edge existed nowhere. This test asserts that
// outcome can no longer happen. It intentionally duplicates none of
// TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost's extra
// third-round-trip coverage; it exists so the issue's literal acceptance
// command finds a test by this precise name, independent of any other test.
func TestCompactNormalTruncateThenAppendThenCompactAgain(t *testing.T) {
	dir := t.TempDir()
	graphPath := filepath.Join(dir, "graph.dat")
	logRoot := filepath.Join(dir, "edgelogs")

	l, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	const source, target = 42, 4242

	// AppendEdge(weight=3).
	first := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 3, LastUpdated: 100}
	if err := l.AppendEdge(source, first); err != nil {
		t.Fatalf("AppendEdge #1 (weight=3): %v", err)
	}

	// Compact (fully successful - no failure injected anywhere in this test).
	if _, err := Compact(graphPath, l); err != nil {
		t.Fatalf("first Compact: unexpected error: %v", err)
	}
	afterFirst, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after first Compact: %v", err)
	}
	assertNeighbors(t, afterFirst.Neighbors(source), []CSREdge{first})

	// The node's own edge log must have been truncated by the successful
	// Compact above (durably reflected in graph.dat now).
	loggedAfterFirst, err := l.ReadNode(source)
	if err != nil {
		t.Fatalf("ReadNode after first Compact: %v", err)
	}
	if len(loggedAfterFirst) != 0 {
		t.Fatalf("expected node %d's edge log truncated to empty after successful Compact, got %+v", source, loggedAfterFirst)
	}

	// AppendEdge(weight=5) to the SAME node, on the ordinary happy path -
	// no crash, no failure injection, nothing unusual about this second
	// append at all.
	second := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 5, LastUpdated: 200}
	if err := l.AppendEdge(source, second); err != nil {
		t.Fatalf("AppendEdge #2 (weight=5): %v", err)
	}

	// Compact again.
	if _, err := Compact(graphPath, l); err != nil {
		t.Fatalf("second Compact: unexpected error: %v", err)
	}
	afterSecond, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after second Compact: %v", err)
	}

	// The regression: on the buggy code this showed Weight=3 (the second
	// edge silently lost), not 3+5=8.
	want := []CSREdge{{Target: target, Type: EdgeEntityCooccur, Weight: 8, LastUpdated: 200}}
	assertNeighbors(t, afterSecond.Neighbors(source), want)

	// The regression also manifested as the edge existing nowhere at all
	// (neither in graph.dat nor in the edge log) - explicitly confirm the
	// edge log was, correctly, truncated again after this second successful
	// Compact, so the surviving Weight=8 above is not an artifact of the
	// edge log still separately holding the second edge uncompacted.
	loggedAfterSecond, err := l.ReadNode(source)
	if err != nil {
		t.Fatalf("ReadNode after second Compact: %v", err)
	}
	if len(loggedAfterSecond) != 0 {
		t.Fatalf("expected node %d's edge log truncated to empty after second successful Compact, got %+v", source, loggedAfterSecond)
	}
}

// TestCompaction_FailedTruncateRetryThenOrdinarySubsequentAppendsSurvive
// combines both fix cycles in one sequence, targeting the exact seam between
// them: a failed-truncate retry cycle (F1's scenario) followed by normal,
// uneventful subsequent appends and compactions on the SAME node (F2's
// scenario). Regressing either fix in a way that only shows up when the
// other fix's code path has already run for this node (e.g. an off-by-one
// in the segment floor computed from a sidecar-driven retry, rather than
// from a clean first-time truncation) would be caught here even if each
// fix's own dedicated regression test above still passes in isolation.
func TestCompaction_FailedTruncateRetryThenOrdinarySubsequentAppendsSurvive(t *testing.T) {
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

	const source, target = 55, 66

	first := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 3, LastUpdated: 1}
	if err := l.AppendEdge(source, first); err != nil {
		t.Fatalf("AppendEdge #1: %v", err)
	}

	// Ensure the writer is closed (and dropped from the cache) before we
	// lock down the directory, matching the F1 regression test's own setup.
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

	// First Compact: WriteCSR's rename succeeds, but TruncateNode fails
	// (F1's window).
	if _, err := Compact(graphPath, l2); err == nil {
		t.Fatalf("expected first Compact to report a truncate-phase error, got nil")
	}
	reloaded1, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after first Compact: %v", err)
	}
	assertNeighbors(t, reloaded1.Neighbors(source), []CSREdge{first})

	// Lift the failure condition and retry - the documented recovery
	// action. The still-un-truncated log entry must not be re-summed, and
	// this retry's TruncateNode call must succeed and correctly record a
	// segment floor (not just delete the directory) this time.
	if err := os.Chmod(nodeDir, 0o755); err != nil {
		t.Fatalf("Chmod restore: %v", err)
	}
	if _, err := Compact(graphPath, l2); err != nil {
		t.Fatalf("retry Compact: unexpected error: %v", err)
	}
	reloaded2, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after retry Compact: %v", err)
	}
	assertNeighbors(t, reloaded2.Neighbors(source), []CSREdge{first})

	// Now the F2 scenario, on the very same node that just went through a
	// failed-then-retried truncation: append again and compact again,
	// twice, exactly as TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost
	// does. Both edges must be reflected, not silently dropped.
	second := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 5, LastUpdated: 2}
	if err := l2.AppendEdge(source, second); err != nil {
		t.Fatalf("AppendEdge #2: %v", err)
	}
	if _, err := Compact(graphPath, l2); err != nil {
		t.Fatalf("second (post-retry) Compact: %v", err)
	}
	reloaded3, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after second post-retry Compact: %v", err)
	}
	assertNeighbors(t, reloaded3.Neighbors(source), []CSREdge{
		{Target: target, Type: EdgeEntityCooccur, Weight: 8, LastUpdated: 2},
	})

	third := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 2, LastUpdated: 3}
	if err := l2.AppendEdge(source, third); err != nil {
		t.Fatalf("AppendEdge #3: %v", err)
	}
	if _, err := Compact(graphPath, l2); err != nil {
		t.Fatalf("third (post-retry) Compact: %v", err)
	}
	reloaded4, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after third post-retry Compact: %v", err)
	}
	assertNeighbors(t, reloaded4.Neighbors(source), []CSREdge{
		{Target: target, Type: EdgeEntityCooccur, Weight: 10, LastUpdated: 3},
	})
}

// TestCompactConcurrentAppendNotLost is subtask 4.5.11.2's (issue #49)
// dedicated regression test: it uses compactNodeLockedHook (a test-only
// synchronization seam, nil/no-op in production - see compact.go's doc
// comment on the var) to deterministically force a concurrent AppendEdge
// call to attempt landing on the SAME node Compact is currently processing,
// exactly in the window between Compact's ReadNodeAfter call for that node
// and its later TruncateNode call (which, on the buggy pre-fix code, would
// have re-listed and deleted whatever segment files existed at truncate
// time - including one holding this concurrently-appended record that
// ReadNodeAfter never saw and that was therefore never merged into
// graph.dat).
//
// This test does not merely assert "no panic" or "no data race" (the -race
// detector already covers memory-safety); it asserts the actual durability
// property the issue requires: the concurrently-appended edge must survive
// TruncateNode's removal (not be silently swept up and deleted) and must be
// picked up and correctly merged by a subsequent Compact run.
func TestCompactConcurrentAppendNotLost(t *testing.T) {
	dir := t.TempDir()
	graphPath := filepath.Join(dir, "graph.dat")
	logRoot := filepath.Join(dir, "edgelogs")

	l, err := OpenEdgeLog(logRoot)
	if err != nil {
		t.Fatalf("OpenEdgeLog: %v", err)
	}
	defer l.Close()

	const source, target = 77, 88

	pre := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 3, LastUpdated: 1}
	if err := l.AppendEdge(source, pre); err != nil {
		t.Fatalf("AppendEdge (pre-existing edge): %v", err)
	}

	concurrent := CSREdge{Target: target, Type: EdgeEntityCooccur, Weight: 5, LastUpdated: 2}

	var (
		appendStarted  = make(chan struct{})
		appendFinished = make(chan error, 1)
		once           sync.Once
	)

	// Install the hook: fires exactly once, for the node under test, while
	// Compact still holds that node's lock (immediately after Compact's
	// ReadNodeAfter call for it has returned, before its later TruncateNode
	// call runs). It kicks off a goroutine racing AppendEdge for the SAME
	// node concurrently with the rest of Compact - that goroutine's
	// AppendEdge call must block on EdgeLog's per-node lock (held by Compact)
	// until Compact's TruncateNode call for this node has completed and
	// released it, on the fixed code. On the buggy pre-fix code (no such
	// lock held across the window), the goroutine's append could instead
	// land in the still-current segment file ReadNodeAfter already read,
	// only for TruncateNode's later re-listing to delete it before it was
	// ever merged.
	compactNodeLockedHook = func(id uint64) {
		if id != source {
			return
		}
		once.Do(func() {
			close(appendStarted)
			go func() {
				appendFinished <- l.AppendEdge(source, concurrent)
			}()
			// Give the concurrent AppendEdge goroutine every reasonable
			// chance to actually reach (and, on the buggy code, complete)
			// its append before this hook returns and Compact proceeds to
			// WriteCSR/TruncateNode. On the fixed code this goroutine is
			// blocked on the node lock the entire time regardless, so this
			// is purely about maximizing the buggy code's chance to
			// reproduce the race, not something the fixed code depends on
			// for correctness.
			select {
			case err := <-appendFinished:
				// The buggy code allows this to complete synchronously
				// before TruncateNode ever runs; feed the error back into
				// the channel so the main goroutine's later <-appendFinished
				// still sees it.
				appendFinished <- err
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
	t.Cleanup(func() { compactNodeLockedHook = nil })

	if _, err := Compact(graphPath, l); err != nil {
		t.Fatalf("Compact (with concurrent append racing it): %v", err)
	}

	select {
	case <-appendStarted:
	default:
		t.Fatalf("compactNodeLockedHook never fired for node %d - test did not exercise the intended window", source)
	}

	if err := <-appendFinished; err != nil {
		t.Fatalf("concurrent AppendEdge: %v", err)
	}

	// The concurrently-appended edge must not have been lost: a second
	// Compact call must find it still present in the edge log (not swept up
	// and deleted by the first Compact's TruncateNode call) and merge it
	// correctly.
	if _, err := Compact(graphPath, l); err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	final, err := LoadCSR(graphPath)
	if err != nil {
		t.Fatalf("LoadCSR after second Compact: %v", err)
	}
	want := []CSREdge{{Target: target, Type: EdgeEntityCooccur, Weight: 8, LastUpdated: 2}}
	assertNeighbors(t, final.Neighbors(source), want)

	// The edge log must be fully truncated again after the second Compact -
	// confirming the concurrently-appended edge was durably merged into
	// graph.dat, not left permanently stuck in the log nor (the original bug)
	// silently discarded with no trace anywhere.
	logged, err := l.ReadNode(source)
	if err != nil {
		t.Fatalf("ReadNode after second Compact: %v", err)
	}
	if len(logged) != 0 {
		t.Fatalf("expected node %d's edge log truncated to empty after second Compact, got %+v", source, logged)
	}
}
