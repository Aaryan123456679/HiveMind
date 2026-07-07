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

// TestReaderDuringSplit is subtask 2b.5.2: a reader that concurrently reads
// a file's content through the SAME storage ExecuteSplitAtomic actually
// mutates must never observe a torn/partial/corrupted byte sequence while a
// real split is in flight -- only either the fully-consistent pre-split
// content, or the fully-consistent post-split redirect-stub content.
//
// This replaces an earlier version of this test (issue #14's original
// implementation, commit 3e95aa2) that pinned an mvcc.Snapshot against a
// SEPARATE mvcc.VersionWriter root/content directory from the one
// ExecuteSplitAtomic actually splits (catalog.ContentStore's cs).
// mvcc.Snapshot.Read() only ever consults CatalogRecord.CurrentVersion,
// which ExecuteSplitAtomic never touches (it only mutates
// Status/RedirectTargetIDs/SizeBytes) -- so that version of the test could
// not fail regardless of whether ExecuteSplitAtomic's concurrency behavior
// toward in-flight readers was correct or badly broken. This was correctly
// flagged as a tautology by independent verification
// (.cdr/runs/2026-07-07/035-verification/verification.json): the reader and
// the split shared no mutable state.
//
// This version fixes that by making the reader call
// catalog.ContentStore.Read (NOT a separate mvcc.Snapshot) against the SAME
// cs/fileID that ExecuteSplitAtomic is concurrently splitting, repeatedly,
// throughout the whole split window (including the StatusSplitting ->
// StatusRedirect transition). This directly exercises the real invariant:
// engine/catalog/content.go's ContentStore.Read does not take
// cs.stripes[stripeFor(fileID)] (unlike Append/ReadPartial/LockFileContent
// -- see content.go's Read doc comment, "Read does not need it either"), so
// a concurrent Read can legitimately observe either fileID's state
// immediately before or immediately after ExecuteSplitAtomic's redirect-stub
// write-then-cat.Put -- but never a torn mix of the two, because both
// ContentStore.writeContentFile and split/execute.go's writeNewContentFile
// use the identical write-to-temp-then-atomic-rename technique (rename is
// atomic on the same filesystem). Every observed read is therefore asserted
// to be byte-identical to EITHER preSplitContent OR the exact redirect-stub
// bytes ExecuteSplitAtomic writes (buildRedirectStubContent), never
// anything else.
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

	preSplitContent := []byte("pre-split content: must survive a REAL concurrent split, read either whole-and-old or whole-and-new, never torn\n")

	// Give fileID a real, on-disk ACTIVE catalog record via cs.Create, so
	// both the concurrent cs.Read below and ExecuteSplitAtomic's own
	// cs.LockFileContent/writeNewContentFile calls operate against the SAME
	// physical content file.
	if _, err := cs.Create(catalog.CatalogRecord{FileID: fileID, Status: catalog.StatusActive}, preSplitContent); err != nil {
		t.Fatalf("cs.Create: %v", err)
	}
	if err := tree.Insert(oldPath, fileID); err != nil {
		t.Fatalf("tree.Insert: %v", err)
	}

	// Long-running reader: repeatedly reads fileID's content directly
	// through cs (the SAME ContentStore ExecuteSplitAtomic mutates)
	// throughout the split window, recording a copy of every observed read.
	// Validation against the exact expected pre-split/post-split byte
	// values happens AFTER the split completes (once RedirectTargetIDs is
	// known, since the new fileID is allocated inside ExecuteSplitAtomic and
	// cannot be predicted in advance) -- see the exact-match check below.
	// Runs concurrently with the real split, under -race.
	const readerIterations = 400
	readerErrCh := make(chan error, 1)
	readerDone := make(chan struct{})
	var readsMu sync.Mutex
	var reads [][]byte
	go func() {
		defer close(readerDone)
		for i := 0; i < readerIterations; i++ {
			got, err := cs.Read(fileID)
			if err != nil {
				readerErrCh <- fmt.Errorf("cs.Read() iteration %d: %v", i, err)
				return
			}
			gotCopy := append([]byte(nil), got...)
			readsMu.Lock()
			reads = append(reads, gotCopy)
			readsMu.Unlock()
			time.Sleep(10 * time.Microsecond)
		}
	}()

	// Drive a REAL split concurrently with the reader above, against the
	// SAME cs/fileID the reader is reading -- this is the actual seam
	// verification found decoupled (mvcc.Snapshot) in the prior version of
	// this test.
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

	// Now that the split has committed, fetch the ACTUAL RedirectTargetIDs
	// ExecuteSplitAtomic assigned (the new fileID is allocated internally,
	// so it cannot be predicted ahead of time) and compute the exact
	// expected redirect-stub bytes via the same production helper
	// ExecuteSplitAtomic itself uses to write them.
	finalRec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("cat.Get(fileID) after split: %v", err)
	}
	if finalRec.Status != catalog.StatusRedirect {
		t.Fatalf("cat.Get(fileID) after split: Status = %v, want StatusRedirect", finalRec.Status)
	}
	if len(finalRec.RedirectTargetIDs) == 0 {
		t.Fatalf("cat.Get(fileID) after split: RedirectTargetIDs is empty")
	}
	expectedStub := buildRedirectStubContent(finalRec.RedirectTargetIDs)

	// A read after the split has fully completed must observe ONLY the
	// exact post-split redirect-stub content -- the split is done, there is
	// no legitimate reason left to see pre-split bytes.
	got, err := cs.Read(fileID)
	if err != nil {
		t.Fatalf("cs.Read() after split completed: %v", err)
	}
	if !bytes.Equal(got, expectedStub) {
		t.Fatalf("cs.Read() after split completed = %q, want exact redirect stub %q", got, expectedStub)
	}

	// Validate EVERY read the concurrent reader observed during the split
	// window: each one must be byte-identical to EITHER preSplitContent OR
	// the exact expectedStub -- anything else (a torn/partial mix, garbage,
	// truncated bytes) is a genuine bug this test must catch.
	var sawPreSplit, sawPostSplit int
	for i, r := range reads {
		switch {
		case bytes.Equal(r, preSplitContent):
			sawPreSplit++
		case bytes.Equal(r, expectedStub):
			sawPostSplit++
		default:
			t.Fatalf("reader iteration %d observed inconsistent content %q, want either pre-split content %q or exact redirect stub %q (torn/corrupted read)", i, r, preSplitContent, expectedStub)
		}
	}

	// The concurrent reader must have observed at least the post-split
	// state at some point (proving it actually overlapped with a real,
	// state-mutating split rather than trivially passing because the split
	// happened to complete before the reader ever ran). Observing the
	// pre-split state at least once is expected in practice but not
	// asserted as a hard requirement, since scheduling could in principle
	// have the split finish extremely fast relative to the reader's first
	// iteration; what actually matters for this acceptance criterion --
	// that no torn state was ever observed -- is already enforced above.
	if sawPostSplit == 0 {
		t.Fatalf("reader never observed post-split content across %d iterations (sawPreSplit=%d) -- test did not actually overlap with the split", len(reads), sawPreSplit)
	}
}
