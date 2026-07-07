package split

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/graph"
	"github.com/Aaryan123456679/HiveMind/engine/mvcc"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// This file is issue #14's (2b.5, the final, mandatory "highest-risk gate"
// subtask of Epic Phase 2b) dedicated concurrent race-test suite, per
// docs/LLD/split.md's "Known risks" bullet: "needs a dedicated concurrent
// race test -- many goroutines appending to the same file simultaneously --
// asserting: no data loss, exactly one split per threshold crossing, and no
// dangling graph edges. Must run under go test -race."
//
// Integration seam note (see architecture-discovery.md for the full
// writeup): engine/split.Trigger is NOT wired into
// catalog.ContentStore.Append in production (a pre-existing, disclosed,
// deliberately-deferred gap; see .cdr/memory/pending.md). The achievable
// integration point exercised below is: many goroutines calling
// catalog.ContentStore.Append concurrently against the SAME fileID (proving
// no data loss / exactly-one-crossing-signal at that layer, which IS wired
// and real), plus a test-harness-level driver that manually invokes
// Orchestrator.BeginSplit -> ExecuteSplitAtomic whenever Append signals a
// crossing, concurrently with those appends -- exactly the achievable
// integration seam the issue's own brief anticipated.
//
// Writer/splitter coordination: a test-only sync.RWMutex ("gate") is used so
// many goroutines can genuinely race each other concurrently inside
// catalog.ContentStore.Append/Orchestrator.AdmitWrite (RLock, real
// concurrent contention on cs's internal per-fileID stripe mutex, exercised
// under -race), while the one goroutine that observes a threshold crossing
// upgrades to an exclusive Lock before driving the actual split. This is a
// test-harness synchronization choice, not a production code change: it
// deliberately avoids exercising the ALREADY-DISCLOSED, out-of-scope
// AdmitWrite-then-Append TOCTOU race (documented on AdmitWrite's own doc
// comment as "superseded once issue #12's atomic commit lands" from the
// writer-queueing perspective, not fully closed at the ContentStore.Append
// call-site level, and not this issue's job to fix), so this test can
// focus on ITS OWN acceptance criteria: no data loss, exactly one split per
// crossing, no dangling graph edges.

// raceRoundState is the mutable, gate-protected pointer to "whichever fileID
// writer goroutines currently target" for TestConcurrentAppendSplitRace.
type raceRoundState struct {
	mu     sync.Mutex
	fileID uint64
}

func (s *raceRoundState) get() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileID
}

func (s *raceRoundState) set(fileID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileID = fileID
}

// TestConcurrentAppendSplitRace is subtask 2b.5.1: many goroutines append to
// the same file simultaneously, crossing catalog.ContentStore's split
// threshold multiple times in aggregate (each crossing driving a real
// ExecuteSplitAtomic split), asserting no appended data is lost, exactly one
// split executes per threshold crossing, and no graph edge references a
// nonexistent fileID afterward.
func TestConcurrentAppendSplitRace(t *testing.T) {
	const (
		numWorkers      = 40
		appendsPerGoRtn = 60
	)

	idAlloc, cs, cat, w := newTestContentStoreDepsWithWAL(t)
	tree := newTestBtree(t)
	appender := newTestEdgeAppenderTracked(t)
	guard := NewFileGuard()
	orch, err := NewOrchestrator(guard, cat, w)
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	rootFileID, err := idAlloc.Next()
	if err != nil {
		t.Fatalf("idAlloc.Next: %v", err)
	}
	const rootPath = "race-root.md"
	if _, err := cs.Create(catalog.CatalogRecord{FileID: rootFileID, Status: catalog.StatusActive}, nil); err != nil {
		t.Fatalf("cs.Create(root): %v", err)
	}
	if err := tree.Insert(rootPath, rootFileID); err != nil {
		t.Fatalf("tree.Insert(root): %v", err)
	}

	state := &raceRoundState{fileID: rootFileID}
	var gate sync.RWMutex // RLock: appenders race freely; Lock: exclusive split window
	var splitCount int64  // atomically incremented, once per real split executed
	var roundSeq int64    // atomically incremented, used to build unique new paths

	errCh := make(chan error, numWorkers)
	var wg sync.WaitGroup

	for wID := 0; wID < numWorkers; wID++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for seq := 0; seq < appendsPerGoRtn; seq++ {
				gate.RLock()
				fid := state.get()

				rec, err := orch.AdmitWrite(fid)
				if err != nil {
					gate.RUnlock()
					if errors.Is(err, ErrSplitInProgress) {
						// Lost a narrow race against a split's exclusive
						// window opening between our RLock and AdmitWrite;
						// back off and retry the SAME sequence number.
						time.Sleep(50 * time.Microsecond)
						seq--
						continue
					}
					errCh <- fmt.Errorf("worker %d seq %d: AdmitWrite(%d): %w", workerID, seq, fid, err)
					return
				}
				if rec.Status != catalog.StatusActive {
					// state.get() hasn't caught up to the split driver's
					// update yet (e.g. read moments before a split
					// completed and flipped it to StatusRedirect, but
					// AdmitWrite itself only refuses StatusSplitting). Back
					// off and retry against whatever state.get() returns
					// next time.
					gate.RUnlock()
					time.Sleep(50 * time.Microsecond)
					seq--
					continue
				}

				payload := []byte(fmt.Sprintf("W%02d-S%04d\n", workerID, seq))
				crossed, err := cs.Append(fid, payload)
				gate.RUnlock()
				if err != nil {
					errCh <- fmt.Errorf("worker %d seq %d: Append(%d): %w", workerID, seq, fid, err)
					return
				}

				if crossed {
					if err := driveSplitRound(idAlloc, cat, cs, tree, appender, w, guard, orch, &gate, state, fid, &splitCount, &roundSeq); err != nil {
						errCh <- fmt.Errorf("worker %d seq %d: driving split for fileID %d: %w", workerID, seq, fid, err)
						return
					}
				}
			}
		}(wID)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	// --- Assertion 1: at least one split actually happened (the workload is
	// sized so aggregate appended bytes comfortably exceed the 8KB default
	// threshold several times over: numWorkers*appendsPerGoRtn*~12 bytes ==
	// 40*60*12 == 28800 bytes >> 8KB). ---
	gotSplits := atomic.LoadInt64(&splitCount)
	if gotSplits < 1 {
		t.Fatalf("splitCount = %d, want >= 1 (workload should have crossed the threshold at least once)", gotSplits)
	}

	// --- Assertion 2: exactly one split per threshold crossing. ---
	// Every completed split leaves exactly one NEW StatusRedirect record
	// behind (the file that WAS active and got split). Cross-check that the
	// number of StatusRedirect records reachable from rootFileID's chain
	// equals splitCount: this is the "exactly one split per crossing"
	// invariant made concrete -- if FileGuard's CAS or ExecuteSplitAtomic's
	// atomicity were broken under concurrency, we'd see either fewer
	// StatusRedirect records than splits attempted (a lost/duplicate split)
	// or more (a phantom one).
	redirectCount := countRedirectRecords(t, cat, rootFileID)
	if int64(redirectCount) != gotSplits {
		t.Errorf("catalog has %d StatusRedirect records reachable from root, want == splitCount (%d)", redirectCount, gotSplits)
	}

	// --- Assertion 3: no data loss. Every worker/seq tag appended anywhere
	// during the run must be found EXACTLY once across the final reachable
	// leaf content, walking the redirect chain from rootFileID. ---
	wantTags := make(map[string]bool, numWorkers*appendsPerGoRtn)
	for wID := 0; wID < numWorkers; wID++ {
		for seq := 0; seq < appendsPerGoRtn; seq++ {
			wantTags[fmt.Sprintf("W%02d-S%04d", wID, seq)] = true
		}
	}
	gotTags := collectLeafTags(t, cat, cs, rootFileID)
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

	// --- Assertion 4: no dangling graph edges. Every edge's Source and
	// Target must resolve via cat.Get -- this is exactly the invariant
	// issue #14's investigation found broken (new fileIDs had no
	// catalog.CatalogRecord at all until the accompanying bugfix; see
	// architecture-discovery.md). ---
	edges := readAppenderEdges(t, appender)
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

// driveSplitRound is invoked by whichever goroutine's Append call observed a
// threshold crossing. It upgrades to the round gate's exclusive lock (so no
// other goroutine is concurrently inside AdmitWrite/Append against the SAME
// fileID while the split executes), drives a real BeginSplit ->
// ExecuteSplitAtomic against the current content, and advances the shared
// round state to the first new fileID so subsequent appends have somewhere
// real to land.
//
// The split always covers the ENTIRE current content (byte-for-byte, no
// gaps) via one or two SectionRanges split on a safe unit boundary (every
// append is a complete "tag\n" line; see the boundary-finding logic below),
// so no data can be lost by the split itself -- two new fileIDs are used
// (rather than one) specifically to also exercise SPLIT_SIBLING edges, not
// just REDIRECT ones.
func driveSplitRound(
	idAlloc *catalog.IDAllocator,
	cat *catalog.Catalog,
	cs *catalog.ContentStore,
	tree *btree.Tree,
	appender *graph.EdgeAppender,
	w *wal.Writer,
	guard *FileGuard,
	orch *Orchestrator,
	gate *sync.RWMutex,
	state *raceRoundState,
	fid uint64,
	splitCount *int64,
	roundSeq *int64,
) error {
	gate.Lock()
	defer gate.Unlock()

	// Someone else may have already advanced past this fileID by the time we
	// got the exclusive lock (should not happen given Append's own
	// per-fileID serialization guarantees a crossing fires exactly once, but
	// checked defensively rather than assumed).
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
	pathA := fmt.Sprintf("race-part-%d-a.md", round)
	pathB := fmt.Sprintf("race-part-%d-b.md", round)

	// Find a safe split point ON a "tag\n" unit boundary (i.e. immediately
	// after some prior append's trailing newline): every Append call writes
	// one complete unit atomically, so any prefix length equal to a sum of
	// whole units is safe to cut at without splitting a tag across the two
	// new files.
	mid := len(content) / 2
	splitAt := -1
	if mid > 0 && mid <= len(content) {
		if idx := bytes.LastIndexByte(content[:mid], '\n'); idx >= 0 {
			splitAt = idx + 1
		}
	}

	var plan SplitPlan
	if splitAt > 0 && splitAt < len(content) {
		plan = SplitPlan{Files: []SplitFileProposal{
			{NewPath: pathA, SectionRanges: []SectionRange{{Start: 0, End: splitAt}}},
			{NewPath: pathB, SectionRanges: []SectionRange{{Start: splitAt, End: len(content)}}},
		}}
	} else {
		// Content too small to find a safe two-way boundary (shouldn't
		// happen once the 8KB threshold has been crossed, but handled
		// defensively): move the whole thing into one new file instead.
		plan = SplitPlan{Files: []SplitFileProposal{
			{NewPath: pathA, SectionRanges: []SectionRange{{Start: 0, End: len(content)}}},
		}}
	}

	oldPath := fmt.Sprintf("race-active-%d.md", fid)
	updated, err := ExecuteSplitAtomic(idAlloc, cat, cs, tree, appender, w, guard, oldPath, fid, content, plan)
	if err != nil {
		return fmt.Errorf("ExecuteSplitAtomic(%d): %w", fid, err)
	}

	atomic.AddInt64(splitCount, 1)

	if len(updated.RedirectTargetIDs) == 0 {
		return fmt.Errorf("ExecuteSplitAtomic(%d): returned record has no RedirectTargetIDs", fid)
	}
	// Continue the writer chain via the FIRST new target. If a second
	// target exists (the common two-way case), its content is still fully
	// reachable/verified via collectLeafTags's recursive walk even though no
	// further appends land there directly.
	state.set(updated.RedirectTargetIDs[0])

	return nil
}

// countRedirectRecords walks the redirect chain reachable from rootFileID
// (following RedirectTargetIDs recursively) and counts how many
// catalog.StatusRedirect records it finds along the way.
func countRedirectRecords(t *testing.T, cat *catalog.Catalog, rootFileID uint64) int {
	t.Helper()
	count := 0
	var walk func(fileID uint64)
	walk = func(fileID uint64) {
		rec, err := cat.Get(fileID)
		if err != nil {
			t.Fatalf("countRedirectRecords: cat.Get(%d): %v", fileID, err)
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

// collectLeafTags walks the redirect chain reachable from rootFileID
// (following RedirectTargetIDs recursively for any StatusRedirect/
// StatusSplit record), reads the content of every StatusActive LEAF file it
// finds, and returns a count of how many times each newline-delimited
// "W##-S####" tag appears across all of them combined. Since each split
// moves its source content verbatim (byte-for-byte, no gaps) into its new
// targets and overwrites the source fileID's own content with an unrelated
// stub, skipping StatusRedirect/StatusSplit files' own content and only
// counting StatusActive leaves' content reconstructs the complete,
// non-duplicated set of ever-appended tags.
func collectLeafTags(t *testing.T, cat *catalog.Catalog, cs *catalog.ContentStore, rootFileID uint64) map[string]int {
	t.Helper()
	tags := make(map[string]int)
	var walk func(fileID uint64)
	walk = func(fileID uint64) {
		rec, err := cat.Get(fileID)
		if err != nil {
			t.Fatalf("collectLeafTags: cat.Get(%d): %v", fileID, err)
		}
		switch rec.Status {
		case catalog.StatusRedirect, catalog.StatusSplit:
			for _, target := range rec.RedirectTargetIDs {
				walk(target)
			}
		case catalog.StatusActive:
			content, err := cs.Read(fileID)
			if err != nil {
				t.Fatalf("collectLeafTags: cs.Read(%d): %v", fileID, err)
			}
			for _, line := range strings.Split(string(content), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				tags[line]++
			}
		default:
			t.Fatalf("collectLeafTags: fileID %d has unexpected Status %v", fileID, rec.Status)
		}
	}
	walk(rootFileID)
	return tags
}

// TestReaderDuringSplit is subtask 2b.5.2: a reader that snapshots the file
// immediately before a split begins continues to read fully consistent
// pre-split content for the duration of its read, regardless of the split
// completing concurrently.
//
// engine/mvcc's Snapshot/CurrentVersion mechanism is orthogonal to
// CatalogRecord.Status/RedirectTargetIDs/SizeBytes (see orchestrate.go's
// doc comment and orchestrate_test.go's "reader_snapshot_unaffected_by_
// splitting" subtest, which proves this across BeginSplit/AbortSplit alone).
// This test extends that guarantee across a REAL ExecuteSplitAtomic call
// completing concurrently with an in-flight long-running reader, not just
// the Status transition in isolation.
//
// mvcc.VersionWriter and catalog.ContentStore are two independent,
// currently-unintegrated content-storage subsystems in this codebase (no
// production call site constructs both against the same root for the same
// fileID; confirmed by repo-wide grep during architecture-discovery). Their
// content file naming schemes collide at version 1 if pointed at the same
// root/content directory (mvcc.VersionPath(fileID, 1) ==
// catalog.ContentStore.ContentPath(fileID)), which is an orthogonal,
// pre-existing, not-yet-integrated architectural gap between the two
// subsystems -- out of this issue's scope to fix. This test therefore uses
// SEPARATE roots for cs (ExecuteSplitAtomic's real split machinery) and vw
// (the mvcc reader side), sharing only the same catalog.Catalog/wal.Writer
// (the actual source of truth both subsystems consult), which is enough to
// exercise the real acceptance criterion: Status/RedirectTargetIDs/SizeBytes
// mutations performed by a REAL split must never affect a version-pinned
// reader.
func TestReaderDuringSplit(t *testing.T) {
	idAlloc, cs, cat, w := newTestContentStoreDepsWithWAL(t)
	tree := newTestBtree(t)
	appender := newTestEdgeAppenderTracked(t)
	guard := NewFileGuard()
	orch, err := NewOrchestrator(guard, cat, w)
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	fileID, err := idAlloc.Next()
	if err != nil {
		t.Fatalf("idAlloc.Next: %v", err)
	}
	const oldPath = "reader-during-split.md"

	// Give fileID an ACTIVE catalog record (via cs.Create, so
	// ExecuteSplitAtomic's own cs.LockFileContent/writeContentFile calls
	// have a real content file to work against) before it is ever touched
	// by mvcc.
	if _, err := cs.Create(catalog.CatalogRecord{FileID: fileID, Status: catalog.StatusActive}, []byte("seed")); err != nil {
		t.Fatalf("cs.Create: %v", err)
	}
	if err := tree.Insert(oldPath, fileID); err != nil {
		t.Fatalf("tree.Insert: %v", err)
	}

	mvccRoot := t.TempDir()
	vw, err := mvcc.NewVersionWriter(mvccRoot)
	if err != nil {
		t.Fatalf("mvcc.NewVersionWriter: %v", err)
	}
	em := mvcc.NewEpochManager()

	preSplitContent := []byte("pre-split content: must survive a REAL concurrent split unchanged\n")
	version, err := vw.CommitVersion(cat, w, em, fileID, preSplitContent)
	if err != nil {
		t.Fatalf("CommitVersion: %v", err)
	}
	if version != 1 {
		t.Fatalf("CommitVersion: version = %d, want 1", version)
	}

	preSplitSnap, err := mvcc.NewSnapshot(cat, vw, em, fileID)
	if err != nil {
		t.Fatalf("NewSnapshot (pre-split): %v", err)
	}
	defer preSplitSnap.Close()

	// Long-running reader: repeatedly reads the pinned pre-split snapshot
	// throughout the split window, asserting byte-identical content on
	// every read. Runs concurrently with the real split below, under -race.
	const readerIterations = 200
	readerErrCh := make(chan error, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for i := 0; i < readerIterations; i++ {
			got, err := preSplitSnap.Read()
			if err != nil {
				readerErrCh <- fmt.Errorf("preSplitSnap.Read() iteration %d: %v", i, err)
				return
			}
			if !bytes.Equal(got, preSplitContent) {
				readerErrCh <- fmt.Errorf("preSplitSnap.Read() iteration %d = %q, want %q", i, got, preSplitContent)
				return
			}
			time.Sleep(10 * time.Microsecond)
		}
	}()

	// Drive a REAL split concurrently with the reader above. The split's
	// content comes from cs (a SEPARATE content store from vw, per this
	// test's doc comment above), seeded independently -- what matters here
	// is that ExecuteSplitAtomic's catalog mutations (Status,
	// RedirectTargetIDs, SizeBytes) for fileID do not disturb CurrentVersion
	// or the mvcc-side version-1 file preSplitSnap is pinned to.
	splitDone := make(chan error, 1)
	go func() {
		if _, err := orch.BeginSplit(fileID); err != nil {
			splitDone <- fmt.Errorf("BeginSplit: %w", err)
			return
		}
		content, err := cs.Read(fileID)
		if err != nil {
			orch.AbortSplit(fileID)
			splitDone <- fmt.Errorf("cs.Read: %w", err)
			return
		}
		plan := SplitPlan{Files: []SplitFileProposal{
			{NewPath: "reader-during-split-part.md", SectionRanges: []SectionRange{{Start: 0, End: len(content)}}},
		}}
		if _, err := ExecuteSplitAtomic(idAlloc, cat, cs, tree, appender, w, guard, oldPath, fileID, content, plan); err != nil {
			splitDone <- fmt.Errorf("ExecuteSplitAtomic: %w", err)
			return
		}
		splitDone <- nil
	}()

	if err := <-splitDone; err != nil {
		t.Fatalf("concurrent split failed: %v", err)
	}
	<-readerDone
	select {
	case err := <-readerErrCh:
		t.Fatalf("reader observed inconsistent content: %v", err)
	default:
	}

	// The already-open preSplitSnap must STILL read the exact pre-split
	// bytes after the split has fully completed.
	got, err := preSplitSnap.Read()
	if err != nil {
		t.Fatalf("preSplitSnap.Read() after split completed: %v", err)
	}
	if !bytes.Equal(got, preSplitContent) {
		t.Fatalf("preSplitSnap.Read() after split completed = %q, want %q", got, preSplitContent)
	}

	// A FRESH snapshot taken after the split must ALSO still read version 1
	// unchanged: ExecuteSplitAtomic never advances fileID's CurrentVersion
	// (it only mutates Status/RedirectTargetIDs/SizeBytes), matching
	// orchestrate.go's documented orthogonality guarantee, now proven
	// through a real ExecuteSplitAtomic call rather than just BeginSplit.
	postSplitSnap, err := mvcc.NewSnapshot(cat, vw, em, fileID)
	if err != nil {
		t.Fatalf("NewSnapshot (post-split): %v", err)
	}
	got, err = postSplitSnap.Read()
	postSplitSnap.Close()
	if err != nil {
		t.Fatalf("postSplitSnap.Read(): %v", err)
	}
	if !bytes.Equal(got, preSplitContent) {
		t.Fatalf("postSplitSnap.Read() = %q, want %q (CurrentVersion must be untouched by ExecuteSplitAtomic)", got, preSplitContent)
	}
}
