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
// EndSplit/AbortSplit is automatically reverted once its lease expires,
// rather than permanently blocking future BeginSplit calls for that fileID.
//
// Uses an injected fake clock (WithClock), not a real-time sleep, so
// "advance past the lease timeout" is deterministic and instant.
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
	orch, err := NewOrchestrator(guard, cat, w, WithClock(fakeClock), WithLeaseDuration(lease))
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

	// A subsequent BeginSplit for the same fileID must now succeed: the
	// abandoned SPLITTING record is force-reverted to StatusActive and the
	// stale guard hold released, then this call wins a fresh split attempt.
	rec, err = orch.BeginSplit(fileID)
	if err != nil {
		t.Fatalf("BeginSplit after lease expiry: unexpected error: %v", err)
	}
	if rec.Status != catalog.StatusSplitting {
		t.Fatalf("BeginSplit after lease expiry: Status = %v, want StatusSplitting", rec.Status)
	}

	gotRec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("cat.Get after reclaim: %v", err)
	}
	if gotRec.Status != catalog.StatusSplitting {
		t.Fatalf("cat.Get after reclaim: Status = %v, want StatusSplitting", gotRec.Status)
	}
	if !guard.InProgress(fileID) {
		t.Fatalf("guard.InProgress after reclaim+fresh BeginSplit: want true (this attempt's own hold)")
	}

	// The reclaimed-and-restarted split attempt behaves like any normal
	// one: EndSplit cleanly transitions it out and releases the guard, with
	// no leftover lease/guard state from the abandoned first attempt.
	rec, err = orch.EndSplit(fileID, catalog.StatusSplit)
	if err != nil {
		t.Fatalf("EndSplit after reclaim: unexpected error: %v", err)
	}
	if rec.Status != catalog.StatusSplit {
		t.Fatalf("EndSplit after reclaim: Status = %v, want StatusSplit", rec.Status)
	}
	if guard.InProgress(fileID) {
		t.Fatalf("EndSplit after reclaim: guard still marked InProgress -- leaked guard state")
	}
}
