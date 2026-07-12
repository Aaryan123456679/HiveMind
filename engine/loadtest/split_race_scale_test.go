// Package loadtest: subtask 6.3.4 (issue #32) -- runs the Epic 2b auto-split
// race-test suite's core scenario (task-2b.5.1's
// TestConcurrentAppendSplitRace, engine/split/split_race_test.go) at
// load-test scale, through this package's own Config/WorkFunc/Result harness
// ([Run]).
//
// This deliberately does NOT invent a new scenario. It re-implements the
// SAME race scenario 2b.5.1 already exercises -- many goroutines
// concurrently calling catalog.ContentStore.Append against the same fileID,
// with a test-harness driver invoking split.Orchestrator.BeginSplit ->
// split.ExecuteSplitAtomic whenever a threshold crossing is observed -- and
// asserts the exact same three invariants: no data loss, exactly one split
// per threshold crossing, and no dangling graph edges. See
// docs/LLD/split.md's "Known risks" bullet and split_race_test.go's own doc
// comment for the full rationale; nothing about the invariants or the
// integration seam changes here, only the scale at which they are driven and
// the fact that goroutine spin-up/iteration/latency bookkeeping is delegated
// to this package's own [Run] harness instead of a bare sync.WaitGroup loop,
// per this package's stated purpose of being reused unmodified for exactly
// this kind of test (see doc.go... package doc comment in harness.go).
//
// engine/loadtest cannot import engine/split's _test.go-only helpers (they
// are unexported test-file identifiers, invisible outside package split's
// own test binary), so the fixture setup below is rebuilt from engine/split,
// engine/catalog, engine/btree, engine/graph and engine/wal's EXPORTED
// constructors only -- the same exported entry points those unexported
// helpers themselves wrap (catalog.Open/NewIDAllocator/NewCatalog/
// OpenContentStore, btree.OpenIndexFile/NewNodeStore/NewNodeAllocator/
// NewTree, graph.OpenEdgeAppender/ReadAll, wal.OpenWriter). The split-driving
// logic (driveSplitRound) and the two oracle walks (countRedirectRecords,
// collectLeafTags) are likewise reproduced against those same exported APIs;
// this is intentional duplication (test-file helpers cannot be imported
// across packages in Go), not a scenario rewrite -- compare line-by-line
// against split_race_test.go's TestConcurrentAppendSplitRace and its
// helpers.
//
// Scale rationale ("load-test scale" vs. the original 2b.5.1 test):
//
//	                   original (2b.5.1)   this test (at scale)   factor
//	goroutines (workers)      40                  200                 5x
//	appends per goroutine     60                   24               0.4x
//	total appends            2,400               4,800                2x
//
// This is calibrated from an EMPIRICAL measurement, not guessed: a timed run
// of the unmodified task-2b.5.1 test
// (`go test ./split/... -race -run TestConcurrentAppendSplitRace -v`) on
// this sandbox took 130s (2,400 appends). Each `catalog.ContentStore.Append`
// call is a full read-modify-write of the file's content PLUS a synchronous,
// fsync'd WAL record (see content.go's `Append`) -- i.e. wall-clock time here
// is dominated by total append COUNT, not by worker count, because every
// append against the single currently-active fileID already fully serializes
// through the same `cs.stripes[stripe]` mutex regardless of how many
// goroutines are contending for it. A first attempt at this test used 400
// workers x 120 appends/worker (20x total appends over the original) and hit
// `go test`'s 10-minute default timeout without finishing (confirmed via a
// full goroutine-stack dump at the deadline, not a genuine deadlock) --
// 48,000 appends at ~54ms/append (130s / 2,400) extrapolates to roughly 40+
// minutes, which is not viable for a CI-friendly regression test.
//
// This version instead scales the GOROUTINE COUNT up sharply (5x: 40 -> 200)
// -- which is what actually stresses this scenario's real concurrency
// surface (FileGuard's CAS under contention, the gate sync.RWMutex's
// reader/writer fairness, Orchestrator.AdmitWrite's status-read races) --
// while keeping total append volume to a modest 2x the original (2,400 ->
// 4,800, via fewer iterations per goroutine) so wall-clock stays bounded by
// the same fsync-dominated cost model that governed the original test:
// expected runtime is roughly 4,800 x ~54ms =~ 260s (about 4-5 minutes),
// comfortably under the 10-minute default `go test` timeout with headroom
// for -race's own scheduling overhead, while genuinely exercising 5x as many
// concurrent goroutines racing the same fileID/gate/guard as task-2b.5.1
// did, for twice as much total work. The invariants under test (exactly-
// one-split-per-crossing via FileGuard's CAS, no-data-loss via the
// append/split content walk, no-dangling-edges via the catalog/graph walk)
// are all-or-nothing correctness properties that a genuinely higher
// goroutine count is well-suited to stress-test even without a
// proportionally larger total-append multiplier.
package loadtest

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/graph"
	"github.com/Aaryan123456679/HiveMind/engine/split"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// --- Fixture setup, rebuilt from exported constructors only (see package
// doc comment above for why this can't just import split_race_test.go's
// unexported helpers). ---

func newScaleWAL(t *testing.T, dir string) *wal.Writer {
	t.Helper()
	w, err := wal.OpenWriter(filepath.Join(dir, "wal"), 1<<20)
	if err != nil {
		t.Fatalf("wal.OpenWriter: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("wal.Writer.Close: %v", err)
		}
	})
	return w
}

func newScaleContentStoreDeps(t *testing.T) (*catalog.IDAllocator, *catalog.ContentStore, *catalog.Catalog, *wal.Writer) {
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

	idAlloc, err := catalog.NewIDAllocator(fm)
	if err != nil {
		t.Fatalf("catalog.NewIDAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := idAlloc.Close(); err != nil {
			t.Errorf("IDAllocator.Close: %v", err)
		}
	})

	cat := catalog.NewCatalog(fm)
	w := newScaleWAL(t, root)

	cs, err := catalog.OpenContentStore(root, cat, w)
	if err != nil {
		t.Fatalf("catalog.OpenContentStore: %v", err)
	}

	return idAlloc, cs, cat, w
}

func newScaleBtree(t *testing.T) *btree.Tree {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.idx")
	f, err := btree.OpenIndexFile(path)
	if err != nil {
		t.Fatalf("btree.OpenIndexFile: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	store := btree.NewNodeStore(f)
	alloc, err := btree.NewNodeAllocator(store)
	if err != nil {
		t.Fatalf("btree.NewNodeAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := alloc.Close(); err != nil {
			t.Errorf("NodeAllocator.Close: %v", err)
		}
	})

	return btree.NewTree(store, alloc, 0)
}

// scaleAppenderDirs mirrors split_race_test.go's own appenderDirs
// bookkeeping map, needed because graph.EdgeAppender doesn't expose its
// backing directory publicly.
var scaleAppenderDirs = map[*graph.EdgeAppender]string{}
var scaleAppenderDirsMu sync.Mutex

func newScaleEdgeAppenderTracked(t *testing.T) *graph.EdgeAppender {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "edges")
	appender, err := graph.OpenEdgeAppender(dir)
	if err != nil {
		t.Fatalf("graph.OpenEdgeAppender: %v", err)
	}
	t.Cleanup(func() {
		if err := appender.Close(); err != nil {
			t.Errorf("EdgeAppender.Close: %v", err)
		}
		scaleAppenderDirsMu.Lock()
		delete(scaleAppenderDirs, appender)
		scaleAppenderDirsMu.Unlock()
	})
	scaleAppenderDirsMu.Lock()
	scaleAppenderDirs[appender] = dir
	scaleAppenderDirsMu.Unlock()
	return appender
}

func readScaleAppenderEdges(t *testing.T, appender *graph.EdgeAppender) []graph.Edge {
	t.Helper()
	scaleAppenderDirsMu.Lock()
	dir, ok := scaleAppenderDirs[appender]
	scaleAppenderDirsMu.Unlock()
	if !ok {
		t.Fatalf("no known directory for appender %p; use newScaleEdgeAppenderTracked", appender)
	}
	edges, err := graph.ReadAll(dir)
	if err != nil {
		t.Fatalf("graph.ReadAll(%s): %v", dir, err)
	}
	return edges
}

// scaleRoundState mirrors split_race_test.go's raceRoundState: the mutable,
// gate-protected pointer to "whichever fileID writer goroutines currently
// target".
type scaleRoundState struct {
	mu     sync.Mutex
	fileID uint64
}

func (s *scaleRoundState) get() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileID
}

func (s *scaleRoundState) set(fileID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileID = fileID
}

// driveSplitRoundAtScale mirrors split_race_test.go's driveSplitRound
// exactly (same locking discipline, same boundary-safe split-point search,
// same two-way SPLIT_SIBLING split), rebuilt against exported APIs only.
func driveSplitRoundAtScale(
	idAlloc *catalog.IDAllocator,
	cat *catalog.Catalog,
	cs *catalog.ContentStore,
	tree *btree.Tree,
	appender *graph.EdgeAppender,
	w *wal.Writer,
	guard *split.FileGuard,
	orch *split.Orchestrator,
	gate *sync.RWMutex,
	state *scaleRoundState,
	fid uint64,
	splitCount *int64,
	roundSeq *int64,
) error {
	gate.Lock()
	defer gate.Unlock()

	if state.get() != fid {
		return nil
	}

	if _, err := orch.BeginSplit(fid); err != nil {
		return fmt.Errorf("BeginSplit(%d): %w", fid, err)
	}

	content, err := cs.Read(fid)
	if err != nil {
		if _, abortErr := orch.AbortSplit(fid); abortErr != nil {
			return fmt.Errorf("cs.Read(%d): %w (AbortSplit also failed: %v)", fid, err, abortErr)
		}
		return fmt.Errorf("cs.Read(%d): %w", fid, err)
	}

	round := atomic.AddInt64(roundSeq, 1)
	pathA := fmt.Sprintf("scale-part-%d-a.md", round)
	pathB := fmt.Sprintf("scale-part-%d-b.md", round)

	mid := len(content) / 2
	splitAt := -1
	if mid > 0 && mid <= len(content) {
		if idx := bytes.LastIndexByte(content[:mid], '\n'); idx >= 0 {
			splitAt = idx + 1
		}
	}

	var plan split.SplitPlan
	if splitAt > 0 && splitAt < len(content) {
		plan = split.SplitPlan{Files: []split.SplitFileProposal{
			{NewPath: pathA, SectionRanges: []split.SectionRange{{Start: 0, End: splitAt}}},
			{NewPath: pathB, SectionRanges: []split.SectionRange{{Start: splitAt, End: len(content)}}},
		}}
	} else {
		plan = split.SplitPlan{Files: []split.SplitFileProposal{
			{NewPath: pathA, SectionRanges: []split.SectionRange{{Start: 0, End: len(content)}}},
		}}
	}

	oldPath := fmt.Sprintf("scale-active-%d.md", fid)
	updated, err := split.ExecuteSplitAtomic(idAlloc, cat, cs, tree, appender, w, guard, oldPath, fid, content, plan)
	if err != nil {
		return fmt.Errorf("ExecuteSplitAtomic(%d): %w", fid, err)
	}

	atomic.AddInt64(splitCount, 1)

	if len(updated.RedirectTargetIDs) == 0 {
		return fmt.Errorf("ExecuteSplitAtomic(%d): returned record has no RedirectTargetIDs", fid)
	}
	state.set(updated.RedirectTargetIDs[0])

	return nil
}

// countScaleRedirectRecords mirrors split_race_test.go's
// countRedirectRecords.
func countScaleRedirectRecords(t *testing.T, cat *catalog.Catalog, rootFileID uint64) int {
	t.Helper()
	count := 0
	var walk func(fileID uint64)
	walk = func(fileID uint64) {
		rec, err := cat.Get(fileID)
		if err != nil {
			t.Fatalf("countScaleRedirectRecords: cat.Get(%d): %v", fileID, err)
		}
		if rec.Status == catalog.StatusRedirect || rec.Status == catalog.StatusSplit {
			count++
			for _, target := range rec.RedirectTargetIDs {
				walk(target)
			}
		}
	}
	walk(rootFileID)
	return count
}

// collectScaleLeafTags mirrors split_race_test.go's collectLeafTags.
func collectScaleLeafTags(t *testing.T, cat *catalog.Catalog, cs *catalog.ContentStore, rootFileID uint64) map[string]int {
	t.Helper()
	tags := make(map[string]int)
	var walk func(fileID uint64)
	walk = func(fileID uint64) {
		rec, err := cat.Get(fileID)
		if err != nil {
			t.Fatalf("collectScaleLeafTags: cat.Get(%d): %v", fileID, err)
		}
		switch rec.Status {
		case catalog.StatusRedirect, catalog.StatusSplit:
			for _, target := range rec.RedirectTargetIDs {
				walk(target)
			}
		case catalog.StatusActive:
			content, err := cs.Read(fileID)
			if err != nil {
				t.Fatalf("collectScaleLeafTags: cs.Read(%d): %v", fileID, err)
			}
			for _, line := range strings.Split(string(content), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				tags[line]++
			}
		default:
			t.Fatalf("collectScaleLeafTags: fileID %d has unexpected Status %v", fileID, rec.Status)
		}
	}
	walk(rootFileID)
	return tags
}

// TestAutoSplitRaceAtScale is subtask 6.3.4 (issue #32): task-2b.5.1's
// TestConcurrentAppendSplitRace scenario (engine/split/split_race_test.go),
// re-run at load-test scale through this package's [Run] harness. See the
// package doc comment above for the exact scale factors and why they were
// chosen, and split_race_test.go's own doc comment for the full integration-
// seam rationale this test reuses unchanged.
//
// Asserts the SAME invariants task-2b.5.1 asserts, no more and no less: at
// least one split occurred, exactly one split per threshold crossing
// (redirectCount == splitCount), no data loss (every appended worker/seq tag
// found exactly once across reachable leaf content), and no dangling graph
// edges (every edge's Source/Target resolves via cat.Get).
func TestAutoSplitRaceAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load-test-scale auto-split race test in -short mode")
	}

	const (
		numWorkers      = 200 // 5x task-2b.5.1's 40 workers
		appendsPerGoRtn = 24  // total appends (numWorkers*appendsPerGoRtn) == 2x task-2b.5.1's 2,400
	)

	idAlloc, cs, cat, w := newScaleContentStoreDeps(t)
	tree := newScaleBtree(t)
	appender := newScaleEdgeAppenderTracked(t)
	guard := split.NewFileGuard()
	orch, err := split.NewOrchestrator(guard, cat, w)
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	rootFileID, err := idAlloc.Next()
	if err != nil {
		t.Fatalf("idAlloc.Next: %v", err)
	}
	const rootPath = "scale-root.md"
	if _, err := cs.Create(catalog.CatalogRecord{FileID: rootFileID, Status: catalog.StatusActive}, nil); err != nil {
		t.Fatalf("cs.Create(root): %v", err)
	}
	if err := tree.Insert(rootPath, rootFileID); err != nil {
		t.Fatalf("tree.Insert(root): %v", err)
	}

	state := &scaleRoundState{fileID: rootFileID}
	var gate sync.RWMutex
	var splitCount int64
	var roundSeq int64

	var mu sync.Mutex
	var workErrs []error

	work := func(workerID, seq int) (time.Duration, error) {
		start := time.Now()
		for {
			gate.RLock()
			fid := state.get()

			rec, err := orch.AdmitWrite(fid)
			if err != nil {
				gate.RUnlock()
				if errors.Is(err, split.ErrSplitInProgress) {
					// Lost a narrow race against a split's exclusive window
					// opening between our RLock and AdmitWrite; back off and
					// retry the SAME (workerID, seq) pair -- mirrors task-
					// 2b.5.1's "seq--; continue" retry exactly, just
					// structured as an in-call loop since the harness's
					// WorkFunc contract invokes each (workerID, iter) pair
					// exactly once.
					time.Sleep(50 * time.Microsecond)
					continue
				}
				return time.Since(start), fmt.Errorf("worker %d seq %d: AdmitWrite(%d): %w", workerID, seq, fid, err)
			}
			if rec.Status != catalog.StatusActive {
				gate.RUnlock()
				time.Sleep(50 * time.Microsecond)
				continue
			}

			payload := []byte(fmt.Sprintf("W%03d-S%05d\n", workerID, seq))
			crossed, err := cs.Append(fid, payload)
			gate.RUnlock()
			if err != nil {
				return time.Since(start), fmt.Errorf("worker %d seq %d: Append(%d): %w", workerID, seq, fid, err)
			}

			if crossed {
				if err := driveSplitRoundAtScale(idAlloc, cat, cs, tree, appender, w, guard, orch, &gate, state, fid, &splitCount, &roundSeq); err != nil {
					return time.Since(start), fmt.Errorf("worker %d seq %d: driving split for fileID %d: %w", workerID, seq, fid, err)
				}
			}
			return time.Since(start), nil
		}
	}

	cfg := Config{
		Workers:    numWorkers,
		Iterations: appendsPerGoRtn,
		MaxLatency: time.Minute,
	}

	res, runErr := Run(cfg, func(workerID, iter int) (time.Duration, error) {
		lat, err := work(workerID, iter)
		if err != nil {
			mu.Lock()
			workErrs = append(workErrs, err)
			mu.Unlock()
		}
		return lat, err
	})
	if runErr != nil {
		t.Fatalf("loadtest.Run: %v", runErr)
	}

	for _, err := range workErrs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	t.Logf("scale run: workers=%d iterations/worker=%d total=%d elapsed=%s throughput=%.0f/s p50=%s p95=%s p99=%s errors=%d",
		numWorkers, appendsPerGoRtn, res.TotalCount, res.Elapsed, res.Throughput(), res.P50(), res.P95(), res.P99(), res.ErrorCount)

	// --- Assertion 1: at least one split actually happened. Workload is
	// sized so aggregate appended bytes comfortably exceed the 8KB default
	// threshold many times over: numWorkers*appendsPerGoRtn*~12 bytes ==
	// 400*120*12 == 576,000 bytes >> 8KB. ---
	gotSplits := atomic.LoadInt64(&splitCount)
	if gotSplits < 1 {
		t.Fatalf("splitCount = %d, want >= 1 (workload should have crossed the threshold at least once)", gotSplits)
	}

	// --- Assertion 2: exactly one split per threshold crossing. ---
	redirectCount := countScaleRedirectRecords(t, cat, rootFileID)
	if int64(redirectCount) != gotSplits {
		t.Errorf("catalog has %d StatusRedirect records reachable from root, want == splitCount (%d)", redirectCount, gotSplits)
	}

	// --- Assertion 3: no data loss. ---
	wantTags := make(map[string]bool, numWorkers*appendsPerGoRtn)
	for wID := 0; wID < numWorkers; wID++ {
		for seq := 0; seq < appendsPerGoRtn; seq++ {
			wantTags[fmt.Sprintf("W%03d-S%05d", wID, seq)] = true
		}
	}
	gotTags := collectScaleLeafTags(t, cat, cs, rootFileID)
	for tag := range gotTags {
		if !wantTags[tag] {
			t.Errorf("unexpected tag %q found in reachable content (not appended by any worker)", tag)
		}
	}
	for tag := range wantTags {
		if count := gotTags[tag]; count != 1 {
			t.Errorf("tag %q found %d times across reachable leaf content, want exactly 1 (data loss or duplication)", tag, count)
		}
	}

	// --- Assertion 4: no dangling graph edges. ---
	edges := readScaleAppenderEdges(t, appender)
	if len(edges) == 0 {
		t.Fatalf("no graph edges recorded at all; expected SPLIT_SIBLING/REDIRECT edges from %d splits", gotSplits)
	}
	for _, e := range edges {
		if _, err := cat.Get(e.Source); err != nil {
			t.Errorf("dangling edge: Source fileID %d (type %v -> %d) has no catalog record: %v", e.Source, e.Type, e.Target, err)
		}
		if _, err := cat.Get(e.Target); err != nil {
			t.Errorf("dangling edge: Target fileID %d (type %v, from %d) has no catalog record: %v", e.Target, e.Type, e.Source, err)
		}
	}
}
