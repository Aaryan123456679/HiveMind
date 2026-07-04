package mvcc

import (
	"sync"
	"testing"
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
