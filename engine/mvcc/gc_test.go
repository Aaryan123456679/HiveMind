package mvcc

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
)

// TestEpochRefcount exercises EpochManager's refcounting bookkeeping (task-2a.2.1):
// opening/closing overlapping "snapshots" (AcquireCurrentEpoch/Release calls) across
// multiple epochs, asserting the refcount is correct at every intermediate step (not
// just at the end), that it reaches zero exactly when the last referencing acquisition
// releases, that a deliberately-provoked double-release is detected (errors) rather
// than silently corrupting state into a negative count, and that everything holds up
// under concurrent use with -race.
func TestEpochRefcount(t *testing.T) {
	em := NewEpochManager()

	// Step 1: fresh manager starts at epoch 1, nothing acquired yet.
	if got := em.CurrentEpoch(); got != 1 {
		t.Fatalf("CurrentEpoch() = %d, want 1", got)
	}
	if got := em.RefCount(1); got != 0 {
		t.Fatalf("RefCount(1) = %d, want 0", got)
	}

	// Step 2: acquire epoch 1 three times ("three overlapping snapshots"), asserting
	// the refcount after each individual acquire, not just at the end.
	if e := em.AcquireCurrentEpoch(); e != 1 {
		t.Fatalf("AcquireCurrentEpoch() #1 = %d, want epoch 1", e)
	}
	if got := em.RefCount(1); got != 1 {
		t.Fatalf("after 1st acquire: RefCount(1) = %d, want 1", got)
	}
	if e := em.AcquireCurrentEpoch(); e != 1 {
		t.Fatalf("AcquireCurrentEpoch() #2 = %d, want epoch 1", e)
	}
	if got := em.RefCount(1); got != 2 {
		t.Fatalf("after 2nd acquire: RefCount(1) = %d, want 2", got)
	}
	if e := em.AcquireCurrentEpoch(); e != 1 {
		t.Fatalf("AcquireCurrentEpoch() #3 = %d, want epoch 1", e)
	}
	if got := em.RefCount(1); got != 3 {
		t.Fatalf("after 3rd acquire: RefCount(1) = %d, want 3", got)
	}

	// Step 3: advance to epoch 2 (simulating a CommitVersion elsewhere bumping the
	// global epoch), then acquire it twice while epoch 1's snapshots are still open
	// ("overlapping snapshots across epochs").
	if got := em.AdvanceEpoch(); got != 2 {
		t.Fatalf("AdvanceEpoch() = %d, want 2", got)
	}
	if got := em.CurrentEpoch(); got != 2 {
		t.Fatalf("CurrentEpoch() after advance = %d, want 2", got)
	}
	if e := em.AcquireCurrentEpoch(); e != 2 {
		t.Fatalf("AcquireCurrentEpoch() after advance = %d, want epoch 2", e)
	}
	if e := em.AcquireCurrentEpoch(); e != 2 {
		t.Fatalf("AcquireCurrentEpoch() 2nd after advance = %d, want epoch 2", e)
	}
	if got := em.RefCount(2); got != 2 {
		t.Fatalf("RefCount(2) = %d, want 2", got)
	}
	if got := em.RefCount(1); got != 3 {
		t.Fatalf("RefCount(1) after advancing to epoch 2 = %d, want unaffected 3", got)
	}

	// Step 4: release epoch 1's three references in a deliberately non-LIFO order,
	// asserting the refcount at every step and that it hits exactly 0 after the last
	// release (never before, never skipping past).
	if err := em.Release(1); err != nil { // "2nd acquired" conceptually, order doesn't matter for a bare counter
		t.Fatalf("Release(1) #1 failed: %v", err)
	}
	if got := em.RefCount(1); got != 2 {
		t.Fatalf("after 1st release: RefCount(1) = %d, want 2", got)
	}
	if err := em.Release(1); err != nil {
		t.Fatalf("Release(1) #2 failed: %v", err)
	}
	if got := em.RefCount(1); got != 1 {
		t.Fatalf("after 2nd release: RefCount(1) = %d, want 1", got)
	}
	if err := em.Release(1); err != nil {
		t.Fatalf("Release(1) #3 failed: %v", err)
	}
	if got := em.RefCount(1); got != 0 {
		t.Fatalf("after 3rd (final) release: RefCount(1) = %d, want exactly 0", got)
	}

	// Step 5: MinReferencedEpoch reflects the smallest epoch with a live reference at
	// each stage: epoch 1 while any of its 3 refs were alive (implicitly covered
	// above via the acquire ordering), epoch 2 once epoch 1 fully drains, and
	// ok=false once everything is released.
	if min, ok := em.MinReferencedEpoch(); !ok || min != 2 {
		t.Fatalf("MinReferencedEpoch() after epoch 1 drains = (%d, %v), want (2, true)", min, ok)
	}
	if err := em.Release(2); err != nil {
		t.Fatalf("Release(2) #1 failed: %v", err)
	}
	if min, ok := em.MinReferencedEpoch(); !ok || min != 2 {
		t.Fatalf("MinReferencedEpoch() with epoch 2's last ref still alive = (%d, %v), want (2, true)", min, ok)
	}
	if err := em.Release(2); err != nil {
		t.Fatalf("Release(2) #2 (final) failed: %v", err)
	}
	if min, ok := em.MinReferencedEpoch(); ok {
		t.Fatalf("MinReferencedEpoch() after everything released = (%d, %v), want ok=false", min, ok)
	}
	if got := em.RefCount(2); got != 0 {
		t.Fatalf("RefCount(2) after full drain = %d, want 0", got)
	}

	// Step 6: deliberately provoke a double-release on an already-zero epoch and
	// assert it is detected (returns an error) rather than silently going negative.
	if err := em.Release(1); err == nil {
		t.Fatal("Release(1) on an already-zero epoch: want non-nil error (double-release), got nil")
	}
	if got := em.RefCount(1); got != 0 {
		t.Fatalf("RefCount(1) after provoked double-release = %d, want 0 (never negative)", got)
	}
	// Releasing an epoch number that was never acquired at all must also error, not
	// panic or corrupt state.
	if err := em.Release(999); err == nil {
		t.Fatal("Release(999) on a never-acquired epoch: want non-nil error, got nil")
	}
	if got := em.RefCount(999); got != 0 {
		t.Fatalf("RefCount(999) = %d, want 0", got)
	}
}

// TestEpochRefcountConcurrent exercises EpochManager under concurrent
// acquire/release/advance calls across a few epochs, to be run with -race: it asserts
// only the final invariant (every acquire is matched by exactly one release, so every
// refcount must end at exactly 0 and MinReferencedEpoch must report none live), since
// intermediate ordering is nondeterministic under concurrency by design.
func TestEpochRefcountConcurrent(t *testing.T) {
	em := NewEpochManager()

	const goroutines = 20
	const opsPerGoroutine = 50

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				if (id+i)%7 == 0 {
					em.AdvanceEpoch()
				}
				epoch := em.AcquireCurrentEpoch()
				if err := em.Release(epoch); err != nil {
					t.Errorf("goroutine %d op %d: Release(%d) failed: %v", id, i, epoch, err)
				}
			}
		}(g)
	}
	wg.Wait()

	final := em.CurrentEpoch()
	for epoch := uint64(1); epoch <= final; epoch++ {
		if got := em.RefCount(epoch); got != 0 {
			t.Fatalf("RefCount(%d) after all goroutines finished = %d, want 0", epoch, got)
		}
	}
	if min, ok := em.MinReferencedEpoch(); ok {
		t.Fatalf("MinReferencedEpoch() after all goroutines finished = (%d, %v), want ok=false", min, ok)
	}
}

// versionExists reports whether fileID's version v still has a version file on disk.
func versionExists(t *testing.T, vw *VersionWriter, fileID, v uint64) bool {
	t.Helper()
	_, err := os.Stat(vw.VersionPath(fileID, v))
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat version %d for fileID %d: %v", v, fileID, err)
	return false
}

// containsVersion reports whether deleted (RunCompaction's return value) contains v.
func containsVersion(deleted []uint64, v uint64) bool {
	for _, d := range deleted {
		if d == v {
			return true
		}
	}
	return false
}

// TestCompactor exercises subtask 2a.2.2's acceptance criteria end-to-end: a version
// file is deleted only once its epoch's refcount is zero AND it is not the current
// version; the current version is never reclaimed regardless of refcount.
func TestCompactor(t *testing.T) {
	t.Run("no open snapshots reclaims everything reclaimable", func(t *testing.T) {
		dir := t.TempDir()
		vw, err := NewVersionWriter(dir)
		if err != nil {
			t.Fatalf("NewVersionWriter: %v", err)
		}
		cat := newTestCatalog(t)
		w, _ := newTestWAL(t, dir)
		em := NewEpochManager()

		const fileID = uint64(1)
		if err := cat.Put(catalog.CatalogRecord{
			FileID:         fileID,
			CurrentVersion: 0,
			Status:         catalog.StatusActive,
		}); err != nil {
			t.Fatalf("seeding initial catalog record: %v", err)
		}

		var lastVersion uint64
		for i := 1; i <= 4; i++ {
			v, err := vw.CommitVersion(cat, w, em, fileID, []byte(fmt.Sprintf("v%d", i)))
			if err != nil {
				t.Fatalf("CommitVersion #%d: %v", i, err)
			}
			lastVersion = v
		}

		deleted, err := RunCompaction(cat, vw, em, fileID)
		if err != nil {
			t.Fatalf("RunCompaction: %v", err)
		}

		// With no open snapshots, everything except the current version must be
		// reclaimed.
		for v := uint64(1); v < lastVersion; v++ {
			if !containsVersion(deleted, v) {
				t.Fatalf("RunCompaction with no open snapshots: version %d not reclaimed, deleted=%v", v, deleted)
			}
			if versionExists(t, vw, fileID, v) {
				t.Fatalf("version %d file still exists on disk after reclamation", v)
			}
		}
		if containsVersion(deleted, lastVersion) {
			t.Fatalf("RunCompaction reclaimed the CURRENT version %d, want it retained", lastVersion)
		}
		if !versionExists(t, vw, fileID, lastVersion) {
			t.Fatalf("current version %d file missing from disk after RunCompaction", lastVersion)
		}
	})

	t.Run("open snapshot pins its version until closed", func(t *testing.T) {
		dir := t.TempDir()
		vw, err := NewVersionWriter(dir)
		if err != nil {
			t.Fatalf("NewVersionWriter: %v", err)
		}
		cat := newTestCatalog(t)
		w, _ := newTestWAL(t, dir)
		em := NewEpochManager()

		const fileID = uint64(2)
		if err := cat.Put(catalog.CatalogRecord{
			FileID:         fileID,
			CurrentVersion: 0,
			Status:         catalog.StatusActive,
		}); err != nil {
			t.Fatalf("seeding initial catalog record: %v", err)
		}

		// Commit v1, v2, v3.
		for i := 1; i <= 3; i++ {
			if _, err := vw.CommitVersion(cat, w, em, fileID, []byte(fmt.Sprintf("v%d", i))); err != nil {
				t.Fatalf("CommitVersion #%d: %v", i, err)
			}
		}

		// Open (and deliberately do not close yet) a Snapshot pinned to v3, the
		// version that is current right now, BEFORE committing v4.
		snap, err := NewSnapshot(cat, vw, em, fileID)
		if err != nil {
			t.Fatalf("NewSnapshot: %v", err)
		}
		if snap.Version() != 3 {
			t.Fatalf("held snapshot pinned to version %d, want 3", snap.Version())
		}

		// Commit v4, making it current and superseding v3 (and, transitively, v1/v2
		// which were already superseded before the snapshot was even taken).
		v4, err := vw.CommitVersion(cat, w, em, fileID, []byte("v4"))
		if err != nil {
			t.Fatalf("CommitVersion v4: %v", err)
		}
		if v4 != 4 {
			t.Fatalf("expected v4 == 4, got %d", v4)
		}

		deleted, err := RunCompaction(cat, vw, em, fileID)
		if err != nil {
			t.Fatalf("RunCompaction (snapshot open): %v", err)
		}

		// (a) v3's file must NOT be deleted: a live snapshot still references it.
		if containsVersion(deleted, 3) {
			t.Fatalf("RunCompaction reclaimed version 3 while a snapshot pinned to it is still open, deleted=%v", deleted)
		}
		if !versionExists(t, vw, fileID, 3) {
			t.Fatal("version 3 file was removed from disk while a snapshot pinned to it is still open")
		}

		// (b) v1 and v2, superseded strictly before the held snapshot's epoch and
		// referenced by nobody, must be deleted.
		for _, v := range []uint64{1, 2} {
			if !containsVersion(deleted, v) {
				t.Fatalf("RunCompaction did not reclaim version %d, want it reclaimed (unreferenced), deleted=%v", v, deleted)
			}
			if versionExists(t, vw, fileID, v) {
				t.Fatalf("version %d file still exists on disk after reclamation", v)
			}
		}

		// (c) the CURRENT version (v4) must never be reclaimed, regardless of
		// refcount. Assert this explicitly, locking in the acceptance criterion.
		if containsVersion(deleted, 4) {
			t.Fatalf("RunCompaction reclaimed the CURRENT version 4, want it retained regardless of refcount")
		}
		if !versionExists(t, vw, fileID, 4) {
			t.Fatal("current version 4 file missing from disk after RunCompaction")
		}

		// Now close the held-open snapshot (its epoch's refcount drops to 0) and run
		// RunCompaction again.
		if err := snap.Close(); err != nil {
			t.Fatalf("snap.Close(): %v", err)
		}

		deleted2, err := RunCompaction(cat, vw, em, fileID)
		if err != nil {
			t.Fatalf("RunCompaction (after close): %v", err)
		}

		// (d) v3 is now eligible and must be reclaimed on this second pass.
		if !containsVersion(deleted2, 3) {
			t.Fatalf("RunCompaction after closing the held snapshot did not reclaim version 3, deleted=%v", deleted2)
		}
		if versionExists(t, vw, fileID, 3) {
			t.Fatal("version 3 file still exists on disk after closing the pinning snapshot and re-running RunCompaction")
		}

		// Explicit sub-case: even now that every prior version's refcount has
		// dropped to zero, the CURRENT version (v4) is STILL retained on this
		// second RunCompaction call — proving "current is never reclaimed" holds
		// independent of refcount, not just incidentally because a snapshot
		// happened to be open in this test.
		if containsVersion(deleted2, 4) {
			t.Fatalf("RunCompaction reclaimed the CURRENT version 4 on second pass, want it retained regardless of refcount")
		}
		if !versionExists(t, vw, fileID, 4) {
			t.Fatal("current version 4 file missing from disk after second RunCompaction")
		}
	})
}

// TestNewSnapshotClosesEpochAcquireVersionReadRace is a regression test for issue #7
// (subtask 2a.2.2's independent verification, CHANGES_REQUESTED): it exercises the
// exact TOCTOU interleaving that used to let RunCompaction prematurely delete a
// version file still referenced by a live, un-closed Snapshot.
//
// The old (buggy) NewSnapshot read CurrentVersion FIRST and acquired the epoch
// SECOND. This test pauses NewSnapshot in the gap between its (now reordered) two
// steps -- right after the epoch has been acquired but before CurrentVersion is read
// -- and, in that gap, runs a full concurrent CommitVersion (superseding the version
// this Snapshot is about to pin to) followed by RunCompaction to completion. If the
// race were still open, RunCompaction would delete the paused Snapshot's
// soon-to-be-pinned version file before the Snapshot ever resumes and reads it. With
// the fix (epoch acquired first), the proof in read.go's NewSnapshot doc comment
// guarantees this can never happen: the resumed Snapshot's pinned version file must
// still exist and be readable.
func TestNewSnapshotClosesEpochAcquireVersionReadRace(t *testing.T) {
	dir := t.TempDir()
	vw, err := NewVersionWriter(dir)
	if err != nil {
		t.Fatalf("NewVersionWriter: %v", err)
	}
	cat := newTestCatalog(t)
	w, _ := newTestWAL(t, dir)
	em := NewEpochManager()

	const fileID = uint64(99)
	if err := cat.Put(catalog.CatalogRecord{
		FileID:         fileID,
		CurrentVersion: 0,
		Status:         catalog.StatusActive,
	}); err != nil {
		t.Fatalf("seeding initial catalog record: %v", err)
	}

	v1Content := []byte("v1-content-pinned-by-paused-snapshot")
	v1, err := vw.CommitVersion(cat, w, em, fileID, v1Content)
	if err != nil {
		t.Fatalf("CommitVersion (v1): %v", err)
	}
	if v1 != 1 {
		t.Fatalf("CommitVersion (v1) = %d, want 1", v1)
	}

	// Channels orchestrating the interleaving: pause newSnapshotWithHook right after
	// it has acquired the epoch but before it reads CurrentVersion, let a concurrent
	// CommitVersion (v2) + RunCompaction run to completion in that window, then
	// resume the paused NewSnapshot call.
	pausedAfterAcquire := make(chan struct{})
	resumeSnapshot := make(chan struct{})

	snapResult := make(chan *Snapshot, 1)
	snapErr := make(chan error, 1)

	go func() {
		snap, err := newSnapshotWithHook(cat, vw, em, fileID, func() {
			close(pausedAfterAcquire)
			<-resumeSnapshot
		})
		snapResult <- snap
		snapErr <- err
	}()

	// Wait until the epoch has been acquired and NewSnapshot is paused right before
	// reading CurrentVersion.
	<-pausedAfterAcquire

	v2Content := []byte("v2-content-committed-while-snapshot-paused")
	v2, err := vw.CommitVersion(cat, w, em, fileID, v2Content)
	if err != nil {
		t.Fatalf("CommitVersion (v2, concurrent with paused NewSnapshot): %v", err)
	}
	if v2 != 2 {
		t.Fatalf("CommitVersion (v2) = %d, want 2", v2)
	}

	// Run compaction while the paused NewSnapshot has already acquired its epoch (so
	// it counts toward MinReferencedEpoch) but has NOT yet read CurrentVersion. If
	// the race were open, this call would delete v1's file: v1 is no longer current
	// (v2 is), and the paused snapshot's acquired epoch would (under the OLD, buggy
	// ordering) understate the protection v1 needs. Under the fix, the acquired
	// epoch is guaranteed to be < v1's supersededAtEpoch, so v1 must survive.
	if _, err := RunCompaction(cat, vw, em, fileID); err != nil {
		t.Fatalf("RunCompaction (while NewSnapshot paused mid-acquire): %v", err)
	}

	if !versionExists(t, vw, fileID, 1) {
		t.Fatal("version 1 file deleted by RunCompaction while a snapshot was mid-acquire and about to pin to it -- TOCTOU race in NewSnapshot is NOT closed")
	}

	// Let the paused NewSnapshot resume and read CurrentVersion now that v2 is
	// current and RunCompaction has already run once.
	close(resumeSnapshot)

	snap := <-snapResult
	if err := <-snapErr; err != nil {
		t.Fatalf("NewSnapshot (resumed after concurrent v2 commit + RunCompaction): %v", err)
	}

	// The resumed snapshot observed the race window fully play out before its
	// cat.Get ran, so it must be pinned to the NEW current version (v2), not v1 --
	// this is the "reads newer version" branch of the reordering's safety argument,
	// not the primary "reads stale V, epoch still protects it" branch, but both must
	// hold.
	if snap.Version() != 2 {
		t.Fatalf("Snapshot.Version() after resuming = %d, want 2 (CurrentVersion had already advanced by the time cat.Get ran)", snap.Version())
	}

	got, err := snap.Read()
	if err != nil {
		t.Fatalf("Snapshot.Read() after resuming from paused mid-acquire race window: %v", err)
	}
	if string(got) != string(v2Content) {
		t.Fatalf("Snapshot.Read() = %q, want %q", got, v2Content)
	}

	// Sanity check on v1 specifically: it is genuinely still on disk and still
	// byte-for-byte readable via a direct Snapshot pinned to it, proving RunCompaction
	// did not silently corrupt or partially remove it.
	v1Path := vw.VersionPath(fileID, 1)
	v1Bytes, err := os.ReadFile(v1Path)
	if err != nil {
		t.Fatalf("reading version 1's file directly after the race window: %v", err)
	}
	if string(v1Bytes) != string(v1Content) {
		t.Fatalf("version 1 file content = %q, want %q (unchanged)", v1Bytes, v1Content)
	}

	// Now close the resumed snapshot so no snapshot references v1's epoch, and
	// confirm a fresh RunCompaction pass is free to reclaim it -- confirming the
	// earlier survival was due to correct refcounting, not a compaction that simply
	// never runs.
	if err := snap.Close(); err != nil {
		t.Fatalf("snap.Close(): %v", err)
	}

	deleted, err := RunCompaction(cat, vw, em, fileID)
	if err != nil {
		t.Fatalf("RunCompaction (after snapshot closed): %v", err)
	}
	if !containsVersion(deleted, 1) {
		t.Fatalf("RunCompaction after closing the snapshot did not reclaim version 1, deleted=%v", deleted)
	}
	if versionExists(t, vw, fileID, 1) {
		t.Fatal("version 1 file still exists on disk after closing the pinning snapshot and re-running RunCompaction")
	}
}

// TestGCUnderConcurrency is subtask 2a.2.3's acceptance test: it runs writers,
// long-running readers holding open snapshots, and the compactor all concurrently
// against a single shared fileID, and asserts that no active snapshot's pinned
// version is EVER reclaimed out from under it -- neither by returning a hard "not
// found" read error, nor by silently returning torn/wrong content that doesn't match
// what was true at the moment the snapshot was acquired.
//
// This is deliberately the same class of race 2a.2.2's independent verification
// caught in TestNewSnapshotClosesEpochAcquireVersionReadRace (a TOCTOU between
// acquiring a snapshot's epoch and reading its pinned version, exploited by a
// concurrently-running compactor), but exercised here as a broad, long-running
// concurrency stress test rather than a single deterministically-paused
// interleaving: real writers continuously advancing versions, real readers holding
// snapshots open across many concurrent commits and compaction passes, and a real
// compactor looping RunCompaction back-to-back throughout, all racing under -race.
func TestGCUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	vw, err := NewVersionWriter(dir)
	if err != nil {
		t.Fatalf("NewVersionWriter: %v", err)
	}
	cat := newTestCatalog(t)
	w, _ := newTestWAL(t, dir)
	em := NewEpochManager()

	const fileID = uint64(42)
	if err := cat.Put(catalog.CatalogRecord{
		FileID:         fileID,
		CurrentVersion: 0,
		Status:         catalog.StatusActive,
	}); err != nil {
		t.Fatalf("seeding initial catalog record: %v", err)
	}

	// Seed one committed version up front so readers always have something to
	// snapshot from the very start of the concurrent phase, rather than racing
	// against the very first writer commit too.
	if _, err := vw.CommitVersion(cat, w, em, fileID, []byte("seed-v0")); err != nil {
		t.Fatalf("seeding initial CommitVersion: %v", err)
	}

	const (
		numWriters   = 4
		numReaders   = 8
		readerRounds = 15
		testDuration = 1500 * time.Millisecond
	)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Shared, mutex-guarded failure collection: readers append here (rather than
	// calling t.Fatalf directly from inside a goroutine, which would only abort that
	// one goroutine) so every violation across every reader is captured and reported
	// together at the end, per this subtask's spec.
	var (
		failuresMu sync.Mutex
		failures   []string
	)
	recordFailure := func(format string, args ...any) {
		failuresMu.Lock()
		defer failuresMu.Unlock()
		failures = append(failures, fmt.Sprintf(format, args...))
	}

	// Writers: continuously commit new versions for the shared fileID until stop is
	// closed.
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			counter := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				payload := []byte(fmt.Sprintf("writer-%d-commit-%d", writerID, counter))
				if _, err := vw.CommitVersion(cat, w, em, fileID, payload); err != nil {
					recordFailure("writer %d commit %d: CommitVersion failed: %v", writerID, counter, err)
					return
				}
				counter++
			}
		}(i)
	}

	// Compactor: loop RunCompaction back-to-back for the whole test duration,
	// racing directly against the writers and the long-running readers below.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := RunCompaction(cat, vw, em, fileID); err != nil {
				recordFailure("RunCompaction failed: %v", err)
				return
			}
		}
	}()

	// Long-running readers: each round, take a Snapshot, read it once immediately
	// (this is safe/race-free precisely because version files are immutable once
	// written and NewSnapshot's acquire-epoch-before-read ordering guarantees the
	// pinned version cannot be reclaimed while this Snapshot is open -- see
	// read.go's NewSnapshot doc comment), hold the Snapshot open for a deliberately
	// extended window while writers and the compactor keep running, then read again
	// and assert it is BOTH error-free AND byte-for-byte identical to the first
	// read.
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for round := 0; round < readerRounds; round++ {
				select {
				case <-stop:
					return
				default:
				}

				snap, err := NewSnapshot(cat, vw, em, fileID)
				if err != nil {
					recordFailure("reader %d round %d: NewSnapshot failed: %v", readerID, round, err)
					continue
				}

				firstRead, err := snap.Read()
				if err != nil {
					recordFailure("reader %d round %d: initial Read() on version %d failed: %v", readerID, round, snap.Version(), err)
					_ = snap.Close()
					continue
				}
				expected := append([]byte(nil), firstRead...)

				// Hold the snapshot open across several more read rounds and a
				// short sleep, deliberately overlapping many concurrent writer
				// commits and compactor passes.
				for j := 0; j < 3; j++ {
					time.Sleep(time.Millisecond)
					mid, err := snap.Read()
					if err != nil {
						recordFailure("reader %d round %d: mid-hold Read() #%d on version %d failed (version reclaimed while still referenced): %v", readerID, round, j, snap.Version(), err)
						continue
					}
					if string(mid) != string(expected) {
						recordFailure("reader %d round %d: mid-hold Read() #%d on version %d = %q, want %q (torn/wrong content while snapshot still open)", readerID, round, j, snap.Version(), mid, expected)
					}
				}

				finalRead, err := snap.Read()
				if err != nil {
					recordFailure("reader %d round %d: final Read() on version %d failed (version reclaimed while still referenced -- GC correctness violated): %v", readerID, round, snap.Version(), err)
				} else if string(finalRead) != string(expected) {
					recordFailure("reader %d round %d: final Read() on version %d = %q, want %q (content mismatch against acquisition-time read)", readerID, round, snap.Version(), finalRead, expected)
				}

				if err := snap.Close(); err != nil {
					recordFailure("reader %d round %d: snap.Close() on version %d failed: %v", readerID, round, snap.Version(), err)
				}
			}
		}(i)
	}

	// Let the concurrent phase run for a bounded duration, then signal every
	// goroutine to stop and wait for them all to finish.
	time.Sleep(testDuration)
	close(stop)
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("TestGCUnderConcurrency: %d GC correctness violation(s) detected:\n%s", len(failures), fmt.Sprintf("%v", failures))
	}

	// Final sanity check: the store must still be in a fully readable state after
	// the whole concurrent phase, via one last one-shot SnapshotRead of whatever is
	// current now.
	if _, err := SnapshotRead(cat, vw, em, fileID); err != nil {
		t.Fatalf("final SnapshotRead after concurrent phase failed: %v", err)
	}
}
