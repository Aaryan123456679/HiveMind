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

// TestFileGuardRegistryBounded is the acceptance-criteria test named in
// subtask 4.5.3.2's test spec (issue #40). It guards a large number of
// distinct fileIDs, each following the real TryAcquire-then-Release winner
// lifecycle, and asserts the registry does not grow linearly with the total
// number of distinct fileIDs ever guarded -- mirroring
// engine/btree/latch_test.go's TestNodeLatchRegistryBounded for
// NodeStore.latches.
func TestFileGuardRegistryBounded(t *testing.T) {
	g := NewFileGuard()
	const totalFileIDs = 50_000

	for fileID := uint64(0); fileID < totalFileIDs; fileID++ {
		if !g.TryAcquire(fileID) {
			t.Fatalf("expected fresh fileID %d to win TryAcquire", fileID)
		}
		// Interleave a losing TryAcquire (exercising the failure/no-refcount-
		// leak path) and an InProgress probe before releasing.
		if g.TryAcquire(fileID) {
			t.Fatalf("expected second TryAcquire for still-held fileID %d to fail", fileID)
		}
		if !g.InProgress(fileID) {
			t.Fatalf("expected fileID %d to be observed in progress before Release", fileID)
		}
		g.Release(fileID)
	}

	// Every fileID above was fully released, so none should still be
	// pinned by an in-progress flag or an outstanding reference: the
	// registry should have shrunk back down to (near) empty, not grown to
	// totalFileIDs entries.
	const bound = 100
	if size := g.guardRegistrySize(); size > bound {
		t.Fatalf("expected FileGuard registry to stay bounded (<= %d entries) after releasing all %d distinct fileIDs, got %d entries -- registry is growing unboundedly", bound, totalFileIDs, size)
	}
}

// TestFileGuardRegistryRetainsInProgressEntries is an end-to-end/integration
// confirmation that a fileID left in-progress (TryAcquire won, Release never
// called) must NOT be evicted, even while many OTHER fileIDs are guarded
// and released around it. This is the FileGuard analogue of
// engine/btree/latch.go's version==0 gate check -- here the gate is
// !inProgress.
//
// Correction (fix-cycle 1, issue #40 verification): this test does NOT
// isolate the !inProgress clause from the refs==0 clause -- TryAcquire's
// winning path here keeps refs>0 for heldFileID the entire time, so removing
// the !inProgress clause from the eviction gate entirely does not make this
// test fail (mutation-tested; confirmed). It only proves the realistic,
// end-to-end scenario (a genuinely in-flight split survives unrelated
// churn), which is still useful, but the clause-level isolation this test's
// old doc comment claimed to provide is instead covered by
// TestFileGuardEvictionGateInProgressClauseIsolated below.
func TestFileGuardRegistryRetainsInProgressEntries(t *testing.T) {
	g := NewFileGuard()
	const heldFileID = uint64(999_999)

	if !g.TryAcquire(heldFileID) {
		t.Fatalf("expected TryAcquire for heldFileID to succeed")
	}

	const churnFileIDs = 10_000
	for fileID := uint64(0); fileID < churnFileIDs; fileID++ {
		if !g.TryAcquire(fileID) {
			t.Fatalf("expected fresh fileID %d to win TryAcquire", fileID)
		}
		g.Release(fileID)
	}

	if !g.InProgress(heldFileID) {
		t.Fatalf("expected heldFileID to still be recorded in progress after unrelated churn")
	}
	if g.TryAcquire(heldFileID) {
		t.Fatalf("expected heldFileID's guard to still be held (TryAcquire should fail), meaning its entry was not evicted while inProgress==true")
	}

	g.Release(heldFileID)
	if !g.TryAcquire(heldFileID) {
		t.Fatalf("expected heldFileID to be acquirable again after its own Release")
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

// TestFileGuardReleaseOrderingAffectsEvictionProgressNotCorrectness is the
// fix-cycle-1 (issue #40 verification) replacement for the unproven
// "load-bearing, prevents double-acquisition" claim that used to live on
// Release's doc comment. It deterministically replays the REVERSED order
// (releaseGuard called before inProgress is stored false) by driving the
// package-internal releaseGuard/fileSplitState directly -- no goroutine race
// is needed to reach this window, because the window is reachable by plain
// sequential code, which is itself part of what the reversed ordering gets
// wrong (it's not a rare race, it's the deterministic outcome of that order).
//
// It proves both halves of the corrected doc comment:
//  1. The entry is NOT evicted when releaseGuard runs while inProgress is
//     still true (this is the real, verifier-confirmed failure mode:
//     eviction progress breaks, matching TestFileGuardRegistryBounded).
//  2. A TryAcquire attempted during that exact "stuck" window (refs==0,
//     inProgress==true, entry still present) still correctly LOSES -- no
//     double-acquisition ever results, refuting the old doc comment's
//     specific correctness claim.
func TestFileGuardReleaseOrderingAffectsEvictionProgressNotCorrectness(t *testing.T) {
	g := NewFileGuard()
	const fileID = uint64(555_001)

	if !g.TryAcquire(fileID) {
		t.Fatalf("expected TryAcquire to win")
	}

	g.mu.Lock()
	s, ok := g.guards[fileID]
	g.mu.Unlock()
	if !ok {
		t.Fatalf("expected an entry for fileID after a winning TryAcquire")
	}

	// Reversed order: release the guard reference BEFORE clearing inProgress
	// (the real Release always clears inProgress first; this manually
	// reproduces what reversing it would do).
	g.releaseGuard(fileID, s)

	if g.guardRegistrySize() == 0 {
		t.Fatalf("expected the entry to be RETAINED despite refs==0, because inProgress is still true -- eviction progress should break under reversed ordering, matching TestFileGuardRegistryBounded's finding")
	}

	// The critical claim under test: does a concurrent TryAcquire win here?
	if g.TryAcquire(fileID) {
		t.Fatalf("BUG: TryAcquire won during the reversed-order 'stuck' window -- this would be the double-acquisition the old doc comment claimed reversing the order could cause")
	}

	// Finish clearing the flag, exactly as the real (correct, unchanged)
	// Release ordering does, and confirm the guard recovers normally.
	s.inProgress.Store(false)
	if !g.TryAcquire(fileID) {
		t.Fatalf("expected TryAcquire to succeed once inProgress is cleared")
	}
	g.Release(fileID)
}

// TestFileGuardEvictionGateInProgressClauseIsolated isolates the !inProgress
// clause of the eviction gate (refs == 0 && !inProgress.Load()) from the
// refs == 0 clause, independent of TryAcquire's side effect of holding
// refs>0 for as long as inProgress is true (which is what made the older
// TestFileGuardRegistryRetainsInProgressEntries unable to isolate the two
// clauses -- see fix-cycle-1 note on that test).
//
// It constructs the refs==0 && inProgress==true state directly via
// acquireGuard/releaseGuard on a fileID whose inProgress flag was set
// out-of-band (bypassing TryAcquire's CAS entirely), then flips inProgress
// to false and repeats -- same refs transition (1 -> 0) both times, only
// inProgress differs, so any eviction-status difference is attributable
// solely to the !inProgress clause.
func TestFileGuardEvictionGateInProgressClauseIsolated(t *testing.T) {
	g := NewFileGuard()
	const fileID = uint64(555_002)

	// Manufacture the entry directly (bypassing TryAcquire) with
	// inProgress == true.
	s := &fileSplitState{}
	s.inProgress.Store(true)
	g.mu.Lock()
	g.guards[fileID] = s
	s.refs = 1
	g.mu.Unlock()

	// refs: 1 -> 0, inProgress still true.
	g.releaseGuard(fileID, s)
	if g.guardRegistrySize() != 1 {
		t.Fatalf("expected entry retained at refs==0 while inProgress==true (isolated !inProgress clause), registry size = %d", g.guardRegistrySize())
	}

	// Re-acquire (refs 0 -> 1) via the real accessor, then flip inProgress to
	// false and release again (refs 1 -> 0) -- same refs transition as
	// above, only inProgress differs this time.
	reacquired := g.acquireGuard(fileID)
	if reacquired != s {
		t.Fatalf("expected acquireGuard to find the same still-registered entry")
	}
	s.inProgress.Store(false)
	g.releaseGuard(fileID, s)

	if g.guardRegistrySize() != 0 {
		t.Fatalf("expected entry evicted at refs==0 once inProgress==false (isolated !inProgress clause now satisfied), registry size = %d", g.guardRegistrySize())
	}
}
