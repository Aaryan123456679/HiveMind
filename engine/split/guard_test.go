package split

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestSplitInProgressCAS forces many goroutines to race TryAcquire for the
// SAME fileID and asserts exactly one wins (gets true) while all others
// lose (get false). This is the acceptance-criteria test named in issue
// #10's subtask 2b.1.2 test spec.
func TestSplitInProgressCAS(t *testing.T) {
	const goroutines = 500
	const fileID = uint64(42)

	g := NewFileGuard()

	var wins atomic.Int64
	var ready sync.WaitGroup
	var start sync.WaitGroup
	var done sync.WaitGroup

	ready.Add(goroutines)
	start.Add(1)
	done.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			start.Wait() // barrier: maximize actual concurrent contention
			if g.TryAcquire(fileID) {
				wins.Add(1)
			}
		}()
	}

	ready.Wait() // all goroutines launched and waiting at the barrier
	start.Done() // release them all at once
	done.Wait()

	if got := wins.Load(); got != 1 {
		t.Fatalf("expected exactly 1 winner among %d concurrent TryAcquire calls for the same fileID, got %d", goroutines, got)
	}
	if !g.InProgress(fileID) {
		t.Fatalf("expected fileID %d to be recorded in-progress after the winning TryAcquire", fileID)
	}
}

// TestAcquireReleaseReacquire verifies the basic lifecycle: acquire
// succeeds, a second acquire before release fails, release clears the
// flag, and acquiring again afterward succeeds.
func TestAcquireReleaseReacquire(t *testing.T) {
	g := NewFileGuard()
	const fileID = uint64(7)

	if !g.TryAcquire(fileID) {
		t.Fatalf("first TryAcquire should succeed")
	}
	if g.TryAcquire(fileID) {
		t.Fatalf("second TryAcquire before Release should fail")
	}

	g.Release(fileID)

	if !g.TryAcquire(fileID) {
		t.Fatalf("TryAcquire after Release should succeed again")
	}
	if g.TryAcquire(fileID) {
		t.Fatalf("TryAcquire immediately after re-acquire (without Release) should fail")
	}
}

// TestIndependentFileIDs verifies that the guard for one fileID does not
// interfere with the guard for a different fileID, including under
// concurrent access.
func TestIndependentFileIDs(t *testing.T) {
	g := NewFileGuard()
	const fileA = uint64(1)
	const fileB = uint64(2)

	var wg sync.WaitGroup
	var aWon, bWon atomic.Bool
	wg.Add(2)
	go func() {
		defer wg.Done()
		aWon.Store(g.TryAcquire(fileA))
	}()
	go func() {
		defer wg.Done()
		bWon.Store(g.TryAcquire(fileB))
	}()
	wg.Wait()

	if !aWon.Load() {
		t.Fatalf("expected fileA's independent TryAcquire to succeed")
	}
	if !bWon.Load() {
		t.Fatalf("expected fileB's independent TryAcquire to succeed")
	}

	// fileA being in progress must not block a second, distinct fileID.
	const fileC = uint64(3)
	if !g.TryAcquire(fileC) {
		t.Fatalf("expected unrelated fileC TryAcquire to succeed while fileA/fileB are in progress")
	}
}

// TestReleaseWithoutHoldingIsNoOp documents and verifies the chosen design:
// calling Release on a fileID that was never acquired (or already
// released) does not panic and does not prevent a subsequent successful
// TryAcquire.
func TestReleaseWithoutHoldingIsNoOp(t *testing.T) {
	g := NewFileGuard()
	const fileID = uint64(99)

	g.Release(fileID) // must not panic

	if !g.TryAcquire(fileID) {
		t.Fatalf("TryAcquire after a no-op Release on a never-acquired fileID should still succeed")
	}

	g.Release(fileID)
	g.Release(fileID) // double release must also not panic

	if !g.TryAcquire(fileID) {
		t.Fatalf("TryAcquire after redundant Releases should still succeed")
	}
}

// TestInProgressObservability verifies InProgress reflects the current
// state without mutating it.
func TestInProgressObservability(t *testing.T) {
	g := NewFileGuard()
	const fileID = uint64(5)

	if g.InProgress(fileID) {
		t.Fatalf("expected fresh fileID to not be in progress")
	}

	if !g.TryAcquire(fileID) {
		t.Fatalf("TryAcquire should succeed")
	}
	if !g.InProgress(fileID) {
		t.Fatalf("expected fileID to be in progress after TryAcquire")
	}

	g.Release(fileID)
	if g.InProgress(fileID) {
		t.Fatalf("expected fileID to not be in progress after Release")
	}
}
