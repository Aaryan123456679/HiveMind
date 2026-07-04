package mvcc

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
)

// TestConcurrentReadersWriters is subtask 2a.1.5's integration-style race test,
// exercising write.go's CommitVersion (2a.1.1/2a.1.2/2a.1.4) together with read.go's
// NewSnapshot/Read (2a.1.3) concurrently against the SAME fileID, asserting that no
// reader ever observes a torn/partial version: every read's bytes must exactly match
// the full content of some version that was legitimately committed.
//
// Design note on avoiding a race in the test itself (see requirement's "watch out for a
// race in the test itself between committing and registering"): rather than building a
// shared "committed so far" registry that readers and writers would need to
// synchronize around (which would just move the torn-read risk into the test), every
// writer's full sequence of payloads is generated deterministically BEFORE any
// goroutine starts, keyed by (writerID, seq). A reader only ever needs to check that
// the bytes it read are byte-for-byte equal to SOME (writerID, seq) payload in that
// fixed, read-only-during-the-race set — membership in a precomputed, immutable map
// requires no additional locking and cannot itself produce a false positive or false
// negative depending on timing. The on-disk version file's mere existence (verified
// implicitly by Read succeeding) together with an exact content match is what proves
// "this is exactly one committed version's content, not a torn mix of two."
func TestConcurrentReadersWriters(t *testing.T) {
	dir := t.TempDir()
	vw, err := NewVersionWriter(dir)
	if err != nil {
		t.Fatalf("NewVersionWriter: %v", err)
	}
	cat := newTestCatalog(t)
	w, _ := newTestWAL(t, dir)

	const fileID = uint64(777)
	if err := cat.Put(catalog.CatalogRecord{
		FileID:         fileID,
		CurrentVersion: 0,
		Status:         catalog.StatusActive,
	}); err != nil {
		t.Fatalf("seeding initial catalog record: %v", err)
	}

	const numWriters = 12
	const versionsPerWriter = 15
	const numReaders = 12

	// Precompute every payload every writer will ever commit, and the full set of
	// valid payload strings, BEFORE any goroutine starts. This map is only ever read
	// (never written) once the race begins, so readers can safely check membership
	// concurrently with no locking and no risk of observing a partially-populated
	// registry.
	payloads := make([][][]byte, numWriters)
	validSet := make(map[string]bool, numWriters*versionsPerWriter)
	for wID := 0; wID < numWriters; wID++ {
		payloads[wID] = make([][]byte, versionsPerWriter)
		for seq := 0; seq < versionsPerWriter; seq++ {
			// Padded, structured payload: a torn/partial read (short read, or bytes
			// spliced from two different commits) would almost certainly fail this
			// exact-match check, since it embeds writer/seq identity plus filler
			// repeated enough times to make truncation or splicing detectable.
			filler := strings.Repeat(fmt.Sprintf("%d", wID%10), 64)
			payload := []byte(fmt.Sprintf("writer=%02d/seq=%03d/filler=%s/end", wID, seq, filler))
			payloads[wID][seq] = payload
			validSet[string(payload)] = true
		}
	}

	var writerWG sync.WaitGroup
	var writerDone int32 // atomic; flipped to 1 once every writer goroutine has returned

	for wID := 0; wID < numWriters; wID++ {
		writerWG.Add(1)
		go func(wID int) {
			defer writerWG.Done()
			for seq := 0; seq < versionsPerWriter; seq++ {
				if _, err := vw.CommitVersion(cat, w, fileID, payloads[wID][seq]); err != nil {
					t.Errorf("writer %d: CommitVersion seq %d: %v", wID, seq, err)
					return
				}
			}
		}(wID)
	}

	var readerWG sync.WaitGroup
	for rID := 0; rID < numReaders; rID++ {
		readerWG.Add(1)
		go func(rID int) {
			defer readerWG.Done()
			for {
				// Snapshot the "writers finished" flag BEFORE taking our own snapshot
				// of CurrentVersion, so that if we observe finishing == true, we are
				// guaranteed this iteration's read reflects (at least) the fully
				// committed final state — ensuring readers keep racing writers to
				// completion rather than stopping early, while still exiting cleanly.
				finishing := atomic.LoadInt32(&writerDone) == 1

				snap, err := NewSnapshot(cat, vw, fileID)
				if err != nil {
					t.Errorf("reader %d: NewSnapshot: %v", rID, err)
					return
				}

				if snap.Version() > 0 {
					data, err := snap.Read()
					if err != nil {
						t.Errorf("reader %d: Read version %d: %v", rID, snap.Version(), err)
						return
					}
					if !validSet[string(data)] {
						t.Errorf("reader %d: read version %d content %q does not exactly match any committed payload (torn/partial read)", rID, snap.Version(), data)
						return
					}
				}

				if finishing {
					return
				}
			}
		}(rID)
	}

	writerWG.Wait()
	atomic.StoreInt32(&writerDone, 1)
	readerWG.Wait()

	// Sanity check: at least one version file must exist per successful CommitVersion
	// call (this loop runs after all goroutines have returned, so it is not itself
	// part of the concurrent race). Under contention, CommitVersion's documented
	// retry-on-lost-CAS behavior (see write.go's CommitVersion doc comment) can write
	// MORE version files than the number of logical calls — every lost race still
	// durably wrote its own never-reused version file before discovering it lost — so
	// this is a lower bound, not an exact count.
	count, maxVersion := countVersionFiles(t, vw, fileID)
	const wantMinCount = numWriters * versionsPerWriter
	if count < wantMinCount {
		t.Fatalf("countVersionFiles: got %d version files, want >= %d (at least one per successful CommitVersion call)", count, wantMinCount)
	}
	if maxVersion != uint64(count) {
		t.Fatalf("countVersionFiles: got max version %d, want %d (highest version number must equal total version files written, since numbering is contiguous and never reused)", maxVersion, count)
	}

	finalRec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("final cat.Get: %v", err)
	}
	// Per CommitVersion's doc comment: once all concurrent calls have returned, the
	// final CurrentVersion must equal the highest version number that exists on disk
	// for this fileID (the temporally-last CAS winner is always the writer of the
	// highest-numbered version file).
	if finalRec.CurrentVersion != maxVersion {
		t.Fatalf("final CurrentVersion = %d, want %d (highest on-disk version)", finalRec.CurrentVersion, maxVersion)
	}
	finalData, err := SnapshotRead(cat, vw, fileID)
	if err != nil {
		t.Fatalf("final SnapshotRead: %v", err)
	}
	if !validSet[string(finalData)] {
		t.Fatalf("final CurrentVersion %d content %q does not exactly match any committed payload", finalRec.CurrentVersion, finalData)
	}
}
