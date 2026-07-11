package split

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/mvcc"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// newTestCatalog opens a fresh catalog.Catalog backed by an isolated
// t.TempDir() path, mirroring engine/catalog/catalog_test.go's and
// engine/mvcc/write_test.go's helper of the same shape.
func newTestCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.dat")
	fm, err := catalog.Open(path)
	if err != nil {
		t.Fatalf("catalog.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := fm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return catalog.NewCatalog(fm)
}

// newTestWAL opens a wal.Writer rooted at a fresh "wal" subdirectory of dir,
// registering cleanup, mirroring engine/mvcc/write_test.go's helper of the
// same shape.
func newTestWAL(t *testing.T, dir string) *wal.Writer {
	t.Helper()
	walDir := filepath.Join(dir, "wal")
	w, err := wal.OpenWriter(walDir, 1<<20)
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

// putActiveRecord seeds an Active CatalogRecord for fileID directly via
// cat.Put, mirroring how a real caller (e.g. ContentStore.Create) would have
// already created the record before any split machinery ever sees it.
func putActiveRecord(t *testing.T, cat *catalog.Catalog, fileID uint64) {
	t.Helper()
	if err := cat.Put(catalog.CatalogRecord{
		FileID:         fileID,
		CurrentVersion: 0,
		Status:         catalog.StatusActive,
	}); err != nil {
		t.Fatalf("seeding active record for fileID %d: %v", fileID, err)
	}
}

// pollUntil polls cond every millisecond until it reports true, failing the
// test (via t.Fatalf, which calls runtime.Goexit -- safe to call from a
// non-test goroutine's caller here since pollUntil always runs on the test's
// own goroutine) if cond does not become true within a generous bound. Used
// by concurrent_writers_and_split_race to synchronize the test goroutine
// with background AdmitWrite-calling goroutines without a fixed sleep.
func pollUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("pollUntil: condition not met within deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSplittingStatusIsolation is subtask 2b.1.3's acceptance-criteria test
// (issue #10): once a split begins, the catalog record transitions to
// SPLITTING; new writer requests are queued (refused, with a documented
// retry contract) rather than applied; readers holding a pre-split MVCC
// snapshot are unaffected.
func TestSplittingStatusIsolation(t *testing.T) {
	t.Run("sequential_lifecycle", func(t *testing.T) {
		dir := t.TempDir()
		cat := newTestCatalog(t)
		w := newTestWAL(t, dir)
		const fileID = uint64(1)
		putActiveRecord(t, cat, fileID)

		orch, err := NewOrchestrator(NewFileGuard(), cat, w)
		if err != nil {
			t.Fatalf("NewOrchestrator: %v", err)
		}

		rec, err := orch.BeginSplit(fileID)
		if err != nil {
			t.Fatalf("BeginSplit: unexpected error: %v", err)
		}
		if rec.Status != catalog.StatusSplitting {
			t.Fatalf("BeginSplit: Status = %v, want StatusSplitting", rec.Status)
		}
		// Catalog itself must reflect the transition, not just the returned value.
		gotRec, err := cat.Get(fileID)
		if err != nil {
			t.Fatalf("cat.Get after BeginSplit: %v", err)
		}
		if gotRec.Status != catalog.StatusSplitting {
			t.Fatalf("cat.Get after BeginSplit: Status = %v, want StatusSplitting", gotRec.Status)
		}

		if _, err := orch.AdmitWrite(fileID); !errors.Is(err, ErrSplitInProgress) {
			t.Fatalf("AdmitWrite while SPLITTING: err = %v, want ErrSplitInProgress", err)
		}

		rec, err = orch.EndSplit(fileID, catalog.StatusSplit)
		if err != nil {
			t.Fatalf("EndSplit(StatusSplit): unexpected error: %v", err)
		}
		if rec.Status != catalog.StatusSplit {
			t.Fatalf("EndSplit(StatusSplit): Status = %v, want StatusSplit", rec.Status)
		}

		// Writer semantics for an already-SPLIT record belong to issue #12;
		// AdmitWrite's contract is narrowly "refuse exactly while SPLITTING".
		if _, err := orch.AdmitWrite(fileID); err != nil {
			t.Fatalf("AdmitWrite after split completed: unexpected error: %v", err)
		}
	})

	t.Run("abort_returns_to_active", func(t *testing.T) {
		dir := t.TempDir()
		cat := newTestCatalog(t)
		w := newTestWAL(t, dir)
		const fileID = uint64(2)
		putActiveRecord(t, cat, fileID)

		guard := NewFileGuard()
		orch, err := NewOrchestrator(guard, cat, w)
		if err != nil {
			t.Fatalf("NewOrchestrator: %v", err)
		}

		if _, err := orch.BeginSplit(fileID); err != nil {
			t.Fatalf("BeginSplit: %v", err)
		}
		rec, err := orch.AbortSplit(fileID)
		if err != nil {
			t.Fatalf("AbortSplit: unexpected error: %v", err)
		}
		if rec.Status != catalog.StatusActive {
			t.Fatalf("AbortSplit: Status = %v, want StatusActive", rec.Status)
		}
		if guard.InProgress(fileID) {
			t.Fatalf("AbortSplit: guard still marked InProgress -- leaked guard state")
		}

		// Guard was actually released (not leaked): a fresh BeginSplit must succeed.
		if _, err := orch.BeginSplit(fileID); err != nil {
			t.Fatalf("BeginSplit after AbortSplit: unexpected error: %v", err)
		}
	})

	t.Run("second_begin_refused_while_splitting", func(t *testing.T) {
		dir := t.TempDir()
		cat := newTestCatalog(t)
		w := newTestWAL(t, dir)
		const fileID = uint64(3)
		putActiveRecord(t, cat, fileID)

		orch, err := NewOrchestrator(NewFileGuard(), cat, w)
		if err != nil {
			t.Fatalf("NewOrchestrator: %v", err)
		}

		if _, err := orch.BeginSplit(fileID); err != nil {
			t.Fatalf("first BeginSplit: %v", err)
		}
		if _, err := orch.BeginSplit(fileID); !errors.Is(err, ErrAlreadySplitting) {
			t.Fatalf("second BeginSplit: err = %v, want ErrAlreadySplitting", err)
		}

		// Status must still be exactly SPLITTING -- the refused second attempt
		// must not have mutated anything.
		rec, err := cat.Get(fileID)
		if err != nil {
			t.Fatalf("cat.Get: %v", err)
		}
		if rec.Status != catalog.StatusSplitting {
			t.Fatalf("Status after refused second BeginSplit = %v, want StatusSplitting", rec.Status)
		}
	})

	t.Run("end_split_refused_if_not_splitting", func(t *testing.T) {
		dir := t.TempDir()
		cat := newTestCatalog(t)
		w := newTestWAL(t, dir)
		const fileID = uint64(4)
		putActiveRecord(t, cat, fileID)

		orch, err := NewOrchestrator(NewFileGuard(), cat, w)
		if err != nil {
			t.Fatalf("NewOrchestrator: %v", err)
		}

		if _, err := orch.EndSplit(fileID, catalog.StatusSplit); !errors.Is(err, ErrNotSplitting) {
			t.Fatalf("EndSplit without a prior BeginSplit: err = %v, want ErrNotSplitting", err)
		}

		if _, err := orch.EndSplit(fileID, catalog.StatusRedirect); !errors.Is(err, ErrUnexpectedStatus) {
			t.Fatalf("EndSplit with invalid outcome: err = %v, want ErrUnexpectedStatus", err)
		}
	})

	t.Run("reader_snapshot_unaffected_by_splitting", func(t *testing.T) {
		dir := t.TempDir()
		cat := newTestCatalog(t)
		w := newTestWAL(t, dir)
		const fileID = uint64(5)
		putActiveRecord(t, cat, fileID)

		vw, err := mvcc.NewVersionWriter(dir)
		if err != nil {
			t.Fatalf("NewVersionWriter: %v", err)
		}
		em := mvcc.NewEpochManager()

		content := []byte("pre-split content, must survive unchanged")
		version, err := vw.CommitVersion(cat, w, em, fileID, content)
		if err != nil {
			t.Fatalf("CommitVersion: %v", err)
		}
		if version != 1 {
			t.Fatalf("CommitVersion: version = %d, want 1", version)
		}

		// Snapshot taken BEFORE the split begins.
		preSplitSnap, err := mvcc.NewSnapshot(cat, vw, em, fileID)
		if err != nil {
			t.Fatalf("NewSnapshot (pre-split): %v", err)
		}
		defer preSplitSnap.Close()

		orch, err := NewOrchestrator(NewFileGuard(), cat, w)
		if err != nil {
			t.Fatalf("NewOrchestrator: %v", err)
		}
		if _, err := orch.BeginSplit(fileID); err != nil {
			t.Fatalf("BeginSplit: %v", err)
		}

		// The pre-split snapshot must still read the exact pre-split bytes,
		// unaffected by the Status transition to SPLITTING.
		got, err := preSplitSnap.Read()
		if err != nil {
			t.Fatalf("preSplitSnap.Read() after BeginSplit: %v", err)
		}
		if string(got) != string(content) {
			t.Fatalf("preSplitSnap.Read() after BeginSplit = %q, want %q", got, content)
		}

		// A FRESH snapshot taken WHILE still SPLITTING must also read the same
		// unaffected content: Status is orthogonal to CurrentVersion, and no
		// writer could have advanced CurrentVersion during this window (any
		// writer path consulting AdmitWrite would have been refused).
		duringSplitSnap, err := mvcc.NewSnapshot(cat, vw, em, fileID)
		if err != nil {
			t.Fatalf("NewSnapshot (during split): %v", err)
		}
		got, err = duringSplitSnap.Read()
		duringSplitSnap.Close()
		if err != nil {
			t.Fatalf("duringSplitSnap.Read(): %v", err)
		}
		if string(got) != string(content) {
			t.Fatalf("duringSplitSnap.Read() = %q, want %q", got, content)
		}

		if _, err := orch.AbortSplit(fileID); err != nil {
			t.Fatalf("AbortSplit: %v", err)
		}

		// A snapshot taken AFTER transitioning back out of SPLITTING must also
		// read the same content: the Status round-trip never touched it.
		postSplitSnap, err := mvcc.NewSnapshot(cat, vw, em, fileID)
		if err != nil {
			t.Fatalf("NewSnapshot (post-split): %v", err)
		}
		got, err = postSplitSnap.Read()
		postSplitSnap.Close()
		if err != nil {
			t.Fatalf("postSplitSnap.Read(): %v", err)
		}
		if string(got) != string(content) {
			t.Fatalf("postSplitSnap.Read() = %q, want %q", got, content)
		}
	})

	t.Run("concurrent_writers_and_split_race", func(t *testing.T) {
		dir := t.TempDir()
		cat := newTestCatalog(t)
		w := newTestWAL(t, dir)
		const fileID = uint64(6)
		putActiveRecord(t, cat, fileID)

		orch, err := NewOrchestrator(NewFileGuard(), cat, w)
		if err != nil {
			t.Fatalf("NewOrchestrator: %v", err)
		}

		const goroutines = 32
		const attemptsPerGoroutine = 200

		var admitted, refused atomic.Int64
		var badErr atomic.Value // stores the first unexpected error seen, if any

		var wg sync.WaitGroup
		stop := make(chan struct{})

		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					_, err := orch.AdmitWrite(fileID)
					switch {
					case err == nil:
						admitted.Add(1)
					case errors.Is(err, ErrSplitInProgress):
						refused.Add(1)
					default:
						badErr.CompareAndSwap(nil, err)
					}
				}
			}()
		}

		// Give the AdmitWrite goroutines a head start observing the Active
		// state before flipping to SPLITTING and back, so both windows
		// (Active and Splitting) are actually exercised.
		pollUntil(t, func() bool { return admitted.Load() >= int64(attemptsPerGoroutine) })

		if _, err := orch.BeginSplit(fileID); err != nil {
			t.Fatalf("BeginSplit: %v", err)
		}
		pollUntil(t, func() bool { return refused.Load() >= int64(attemptsPerGoroutine) })

		if _, err := orch.AbortSplit(fileID); err != nil {
			t.Fatalf("AbortSplit: %v", err)
		}
		pollUntil(t, func() bool { return admitted.Load() >= int64(2*attemptsPerGoroutine) })

		close(stop)
		wg.Wait()

		if v := badErr.Load(); v != nil {
			t.Fatalf("AdmitWrite returned unexpected error during race: %v", v)
		}
		if admitted.Load() == 0 {
			t.Fatalf("expected at least some admitted AdmitWrite calls, got 0")
		}
		if refused.Load() == 0 {
			t.Fatalf("expected at least some ErrSplitInProgress-refused AdmitWrite calls, got 0")
		}
	})
}

// TestAbandonedSplittingRecoversAfterTimeout subtask 4.5.3.3's
// acceptance-criteria test (issue #40): a catalog record left in
// StatusSplitting because its split holder crashed between BeginSplit and
// EndSplit/AbortSplit has its catalog record automatically reverted once its
// lease expires, unblocking AdmitWrite callers, rather than permanently
// blocking writers for that fileID.
//
// Uses an injected fake clock (withClock), not a real-time sleep, so
// "advance past the lease timeout" is deterministic and instant.
//
// Fix-cycle correction (issue #40 verification, attempt 1): this test's
// assertions were updated to match reclaimIfExpired's corrected,
// guard-preserving design (see orchestrate.go's package doc comment and
// reclaimIfExpired's own doc comment for the full rationale). Previously,
// this test asserted that a SECOND BeginSplit call after lease expiry
// SUCCEEDS and wins a fresh split attempt -- that assertion described
// exactly the unsafe "release the stale guard and let a new caller start a
// concurrent second split" behavior the verification finding identified.
// The corrected behavior asserted below is: the catalog record is reverted
// to StatusActive (so AdmitWrite stops refusing writers) but the guard
// itself is NOT released by the reclaim, so a second BeginSplit still
// correctly returns ErrAlreadySplitting -- only the ORIGINAL holder's own
// (however belated) EndSplit/AbortSplit call can release the guard.
func TestAbandonedSplittingRecoversAfterTimeout(t *testing.T) {
	dir := t.TempDir()
	cat := newTestCatalog(t)
	w := newTestWAL(t, dir)
	const fileID = uint64(7)
	putActiveRecord(t, cat, fileID)

	var nowNanos atomic.Int64
	nowNanos.Store(time.Now().UnixNano())
	fakeClock := func() time.Time { return time.Unix(0, nowNanos.Load()) }

	const lease = 5 * time.Second
	guard := NewFileGuard()
	orch, err := NewOrchestrator(guard, cat, w, withClock(fakeClock), withLeaseDuration(lease))
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	// Simulate a split holder that crashes between BeginSplit and
	// EndSplit/AbortSplit: BeginSplit wins and transitions the record to
	// StatusSplitting, but nothing ever calls EndSplit or AbortSplit for
	// this fileID again -- the guard and the on-disk record are both left
	// exactly as a real crash would leave them.
	rec, err := orch.BeginSplit(fileID)
	if err != nil {
		t.Fatalf("BeginSplit (simulated crash holder): unexpected error: %v", err)
	}
	if rec.Status != catalog.StatusSplitting {
		t.Fatalf("BeginSplit (simulated crash holder): Status = %v, want StatusSplitting", rec.Status)
	}

	// Before the lease has expired, a second BeginSplit must still be
	// refused exactly as task 2b.1.3 established -- the lease timeout must
	// not weaken the existing double-split protection.
	if _, err := orch.BeginSplit(fileID); !errors.Is(err, ErrAlreadySplitting) {
		t.Fatalf("BeginSplit before lease expiry: err = %v, want ErrAlreadySplitting", err)
	}

	// Advance the injected clock past the lease timeout -- no real sleep.
	nowNanos.Add(int64(lease) + int64(time.Second))

	// A subsequent BeginSplit for the same fileID must still be refused:
	// the abandoned SPLITTING record's lease has expired, so this call
	// force-reverts the catalog record to StatusActive (unblocking
	// AdmitWrite), but it must NOT release the guard or let this call (or
	// any other concurrent caller) start a second, concurrent split -- that
	// is precisely the double-acquisition the verification finding
	// identified as unsafe.
	if _, err := orch.BeginSplit(fileID); !errors.Is(err, ErrAlreadySplitting) {
		t.Fatalf("BeginSplit after lease expiry: err = %v, want ErrAlreadySplitting (guard must stay held)", err)
	}

	gotRec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("cat.Get after reclaim: %v", err)
	}
	if gotRec.Status != catalog.StatusActive {
		t.Fatalf("cat.Get after reclaim: Status = %v, want StatusActive (writers unblocked)", gotRec.Status)
	}
	if !guard.InProgress(fileID) {
		t.Fatalf("guard.InProgress after reclaim: want true -- reclaim must not release the guard")
	}

	// AdmitWrite must now succeed: the reclaim unblocked writers even though
	// the guard itself is still (correctly) held.
	if _, err := orch.AdmitWrite(fileID); err != nil {
		t.Fatalf("AdmitWrite after reclaim: unexpected error: %v", err)
	}

	// The true (simulated-crashed) original holder eventually "wakes up"
	// and calls EndSplit. Because its record was already force-reverted out
	// from under it, EndSplit correctly reports ErrNotSplitting (its split
	// attempt was invalidated) rather than silently succeeding against
	// whatever state happens to be there -- but it still releases the guard
	// and clears the lease, exactly as a normal exit would, since FileGuard's
	// "winner eventually calls Release" contract does not depend on the
	// transition itself having succeeded.
	if _, err := orch.EndSplit(fileID, catalog.StatusSplit); !errors.Is(err, ErrNotSplitting) {
		t.Fatalf("EndSplit for the true (reclaimed) holder: err = %v, want ErrNotSplitting", err)
	}
	if guard.InProgress(fileID) {
		t.Fatalf("EndSplit for the true (reclaimed) holder: guard still marked InProgress -- leaked guard state")
	}

	// Only NOW -- after the true holder's own EndSplit call actually
	// released the guard -- can a fresh BeginSplit succeed.
	rec, err = orch.BeginSplit(fileID)
	if err != nil {
		t.Fatalf("BeginSplit after true holder's EndSplit: unexpected error: %v", err)
	}
	if rec.Status != catalog.StatusSplitting {
		t.Fatalf("BeginSplit after true holder's EndSplit: Status = %v, want StatusSplitting", rec.Status)
	}
	if _, err := orch.EndSplit(fileID, catalog.StatusSplit); err != nil {
		t.Fatalf("EndSplit for the fresh attempt: unexpected error: %v", err)
	}
}

// TestReclaimNeverDoubleAcquiresGuardForSlowHolder is this fix-cycle's
// concurrency test (issue #40 verification, attempt 1) proving the
// verification finding's concrete scenario -- a legitimate split holder H
// that is merely slow (not actually crashed) past leaseDuration -- can no
// longer result in a second, concurrent caller C believing it has also won
// the right to split the same fileID while H is still working.
//
// Before this fix-cycle's correction, reclaimIfExpired released the stale
// guard hold on lease expiry, so C's BeginSplit (retried internally after a
// successful reclaim) could win TryAcquire and start executing while H was
// still live, and H's eventual EndSplit would then clobber C's state and
// release C's guard in turn. This test drives exactly that timeline with
// real goroutines (not just a single-threaded sequential replay) and a fake
// clock, and asserts guard.InProgress(fileID) is true for the entire
// window, with C never observing anything other than ErrAlreadySplitting,
// however many times or however long it retries -- i.e. the fix makes
// double-acquisition structurally impossible, not merely less likely.
func TestReclaimNeverDoubleAcquiresGuardForSlowHolder(t *testing.T) {
	dir := t.TempDir()
	cat := newTestCatalog(t)
	w := newTestWAL(t, dir)
	const fileID = uint64(8)
	putActiveRecord(t, cat, fileID)

	var nowNanos atomic.Int64
	nowNanos.Store(time.Now().UnixNano())
	fakeClock := func() time.Time { return time.Unix(0, nowNanos.Load()) }

	const lease = 1 * time.Second
	guard := NewFileGuard()
	orch, err := NewOrchestrator(guard, cat, w, withClock(fakeClock), withLeaseDuration(lease))
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	// H wins the one and only legitimate BeginSplit for fileID.
	if _, err := orch.BeginSplit(fileID); err != nil {
		t.Fatalf("H's BeginSplit: unexpected error: %v", err)
	}

	// Advance the fake clock well past H's lease -- from this point on, any
	// call into reclaimIfExpired for fileID judges H's lease expired, even
	// though H (below) is still genuinely "working", not crashed.
	nowNanos.Add(int64(lease) * 10)

	// C hammers BeginSplit concurrently with H still "in flight", exactly
	// mirroring the verification finding's scenario. If the bug were
	// present, some iteration of this loop would observe err == nil (C
	// believing it won a fresh split) while guard.InProgress(fileID) was
	// already true for H.
	var sawDoubleAcquisition atomic.Bool
	var cWon atomic.Bool
	stopC := make(chan struct{})
	var wgC sync.WaitGroup
	wgC.Add(1)
	go func() {
		defer wgC.Done()
		for {
			select {
			case <-stopC:
				return
			default:
			}
			if _, err := orch.BeginSplit(fileID); err == nil {
				// C's BeginSplit reported success while H's own attempt is
				// still outstanding: a genuine double-acquisition.
				cWon.Store(true)
				sawDoubleAcquisition.Store(true)
				return
			} else if !errors.Is(err, ErrAlreadySplitting) {
				sawDoubleAcquisition.Store(true) // unexpected error shape entirely
				return
			}
		}
	}()

	// Give C's goroutine a real (short) window to hammer BeginSplit while
	// H's lease is expired and H has still not called EndSplit -- this is
	// the exact window the bug required to manifest.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && !sawDoubleAcquisition.Load() {
		time.Sleep(time.Millisecond)
	}
	close(stopC)
	wgC.Wait()

	if sawDoubleAcquisition.Load() {
		t.Fatalf("double-acquisition detected: C's BeginSplit succeeded (or errored unexpectedly) while H's split was still outstanding")
	}
	if cWon.Load() {
		t.Fatalf("C's BeginSplit reported success while H's guard hold was still outstanding")
	}
	if !guard.InProgress(fileID) {
		t.Fatalf("guard.InProgress(fileID) = false after C's hammering -- H's guard hold was released out from under it")
	}

	// H (finally) finishes and calls EndSplit -- even though its record was
	// force-reverted by C's repeated reclaim attempts, EndSplit still
	// releases H's guard at this, the actually-correct, moment.
	if _, err := orch.EndSplit(fileID, catalog.StatusSplit); !errors.Is(err, ErrNotSplitting) {
		t.Fatalf("H's EndSplit: err = %v, want ErrNotSplitting (record was reclaimed while H worked)", err)
	}
	if guard.InProgress(fileID) {
		t.Fatalf("guard.InProgress(fileID) = true after H's EndSplit -- guard leaked")
	}

	// Only now can a fresh BeginSplit legitimately succeed.
	if _, err := orch.BeginSplit(fileID); err != nil {
		t.Fatalf("BeginSplit after H's EndSplit: unexpected error: %v", err)
	}
}

// TestEndSplitClearsLeaseForFreshSubsequentAttempt is this fix-cycle's test
// for the disclosed test-coverage gap (issue #40 verification, attempt 1):
// the original acceptance test never actually observed any effect of
// EndSplit clearing fileID's lease entry, so disabling that clear entirely
// still passed every existing test. This test constructs a scenario where a
// stale (un-cleared) lease entry left behind by a normal EndSplit would
// cause a completely fresh, still-within-its-own-lease BeginSplit attempt to
// be wrongly treated as expired by a later reclaimIfExpired call.
func TestEndSplitClearsLeaseForFreshSubsequentAttempt(t *testing.T) {
	dir := t.TempDir()
	cat := newTestCatalog(t)
	w := newTestWAL(t, dir)
	const fileID = uint64(9)
	putActiveRecord(t, cat, fileID)

	var nowNanos atomic.Int64
	nowNanos.Store(time.Now().UnixNano())
	fakeClock := func() time.Time { return time.Unix(0, nowNanos.Load()) }

	const lease = 5 * time.Second
	guard := NewFileGuard()
	orch, err := NewOrchestrator(guard, cat, w, withClock(fakeClock), withLeaseDuration(lease))
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	// First attempt: begins at t0, its lease deadline is t0+lease.
	if _, err := orch.BeginSplit(fileID); err != nil {
		t.Fatalf("first BeginSplit: %v", err)
	}
	// Ends cleanly, well within its own lease -- EndSplit must clear this
	// attempt's lease entry entirely, not merely leave it stale.
	if _, err := orch.AbortSplit(fileID); err != nil {
		t.Fatalf("first AbortSplit: %v", err)
	}

	// Directly inspect the unexported leases map (this test file is in
	// package split, so it can): if EndSplit's clear did not run, a stale
	// entry for fileID -- with the FIRST attempt's now-past deadline --
	// would still be sitting here.
	orch.mu.Lock()
	_, staleEntryStillPresent := orch.leases[fileID]
	orch.mu.Unlock()
	if staleEntryStillPresent {
		t.Fatalf("leases[fileID] still present immediately after EndSplit -- clearLease did not run")
	}

	// Advance the clock to exactly the moment the FIRST attempt's
	// (already-cleared) deadline would have expired.
	nowNanos.Add(int64(lease) + int64(time.Second))

	// Second, entirely fresh attempt begins now, at t0+lease+1s -- its OWN
	// lease deadline is (t0+lease+1s)+lease, still far in the future.
	if _, err := orch.BeginSplit(fileID); err != nil {
		t.Fatalf("second BeginSplit: %v", err)
	}

	// A concurrent third caller's BeginSplit must be refused via the normal
	// TryAcquire-loses path, with reclaimIfExpired correctly finding nothing
	// to reclaim (the second attempt's own lease is nowhere near expired
	// yet). If the first attempt's stale entry had survived (the mutation
	// this test targets), reclaimIfExpired would instead have found that
	// old, already-expired deadline still sitting in the map and wrongly
	// force-reverted the SECOND (still fully legitimate) attempt's record
	// back to StatusActive.
	if _, err := orch.BeginSplit(fileID); !errors.Is(err, ErrAlreadySplitting) {
		t.Fatalf("third BeginSplit: err = %v, want ErrAlreadySplitting", err)
	}
	rec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("cat.Get: %v", err)
	}
	if rec.Status != catalog.StatusSplitting {
		t.Fatalf("Status after third BeginSplit = %v, want StatusSplitting (second attempt must still be intact, not wrongly reclaimed)", rec.Status)
	}

	if _, err := orch.EndSplit(fileID, catalog.StatusSplit); err != nil {
		t.Fatalf("second attempt's EndSplit: unexpected error: %v", err)
	}
}
