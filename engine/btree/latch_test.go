package btree

import (
	"sync"
	"testing"
)

// TestNodeLatchRegistryBounded is this subtask's (4.5.1.3) primary acceptance
// test: NodeStore.latches must not grow linearly with the total number of
// distinct node IDs ever passed to Lock/Unlock -- it must stay bounded, because
// entries for node IDs that are locked/unlocked without ever being mutated (no
// intervening WriteNode call) are reclaimed once their reference count drops to
// zero (see latch.go's releaseLatch doc comment).
func TestNodeLatchRegistryBounded(t *testing.T) {
	store := newLatchTestStore(t)

	const distinctIDs = 20000

	for id := uint64(1); id <= distinctIDs; id++ {
		store.Lock(id)
		store.Unlock(id)
	}

	got := store.latchRegistrySize()
	// A handful of entries may legitimately remain (e.g. none should, in this
	// exact scenario, since every entry here is locked/unlocked with no
	// intervening WriteNode call -- but bound generously rather than requiring
	// exactly 0, so this test isn't coupled to internal timing details). The
	// important assertion is that it is nowhere near distinctIDs, i.e. the
	// registry is NOT growing one-entry-per-distinct-ID-ever-locked.
	const bound = 100
	if got > bound {
		t.Fatalf("latchRegistrySize() = %d after locking/unlocking %d distinct never-written node IDs, want <= %d (registry must not grow linearly with total distinct node IDs ever locked)", got, distinctIDs, bound)
	}
}

// TestNodeLatchRegistryBoundedConcurrent is TestNodeLatchRegistryBounded's
// concurrent counterpart: many goroutines locking/unlocking many distinct node
// IDs concurrently must still leave the registry bounded, not racing (run under
// -race) and not leaking entries proportional to total operations performed.
func TestNodeLatchRegistryBoundedConcurrent(t *testing.T) {
	store := newLatchTestStore(t)

	const goroutines = 50
	const idsPerGoroutine = 2000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			base := uint64(g*idsPerGoroutine + 1)
			for i := 0; i < idsPerGoroutine; i++ {
				id := base + uint64(i)
				store.Lock(id)
				store.Unlock(id)
			}
		}(g)
	}
	wg.Wait()

	got := store.latchRegistrySize()
	const bound = 500
	if got > bound {
		t.Fatalf("latchRegistrySize() = %d after %d goroutines each locking/unlocking %d distinct never-written node IDs concurrently, want <= %d", got, goroutines, idsPerGoroutine, bound)
	}
}

// TestNodeLatchEvictionPreservesVersionAcrossCycles is the regression test for
// this subtask's central correctness finding (see architecture-discovery.md /
// latch.go's releaseLatch doc comment): evicting a nodeLatch entry must never
// reset a node's version counter once that node has actually been mutated, or
// Tree.Lookup's optimistic read protocol could silently miss a concurrent write.
// This forces exactly the eviction-eligible idle window (refs dropping to zero
// between operations) around repeated Lock-WriteNode-Unlock cycles interleaved
// with plain Version probes and Lock/Unlock probes on OTHER node IDs (to
// encourage the registry to actually reclaim entries in between), and asserts
// the version count is always the exact, monotonically-accumulated total -
// never reset back down.
func TestNodeLatchEvictionPreservesVersionAcrossCycles(t *testing.T) {
	store := newLatchTestStore(t)
	const nodeID = 1
	const mutations = 500

	for i := 0; i < mutations; i++ {
		// Churn a large number of OTHER, unrelated node IDs through
		// Lock/Unlock in between mutations, to make it likely nodeID's own
		// entry (if it were ever evicted) would actually be reclaimed and
		// recreated by the time we check its version again.
		for j := 0; j < 10; j++ {
			other := uint64(1000 + i*10 + j)
			store.Lock(other)
			store.Unlock(other)
		}

		before := store.Version(nodeID)
		if before != uint64(i) {
			t.Fatalf("mutation %d: Version(nodeID) before write = %d, want %d (version must never reset due to registry eviction)", i, before, i)
		}

		store.Lock(nodeID)
		if err := store.WriteNode(nodeID, encodeTestLeaf(t, "auth/login", uint64(i))); err != nil {
			store.Unlock(nodeID)
			t.Fatalf("mutation %d: WriteNode: %v", i, err)
		}
		store.Unlock(nodeID)

		after := store.Version(nodeID)
		if after != uint64(i+1) {
			t.Fatalf("mutation %d: Version(nodeID) after write = %d, want %d (version must never reset due to registry eviction)", i, after, i+1)
		}
	}

	if got := store.Version(nodeID); got != uint64(mutations) {
		t.Fatalf("final Version(nodeID) = %d, want %d", got, mutations)
	}
}

// TestNodeLatchNoDoubleLockAcrossEviction is a direct regression test for the
// hazard latch.go's Unlock doc comment calls out explicitly: eviction must never
// let two goroutines simultaneously believe they hold the same node ID's latch
// (which would be a total mutual-exclusion failure). Many goroutines
// repeatedly Lock/increment-a-shared-unprotected-counter/Unlock the SAME small
// set of node IDs (small enough, and with high enough iteration count, that
// registry eviction/recreation for these exact IDs is very likely to occur
// between iterations); a data race on the shared counter (caught by -race) or a
// final count different from the expected total would indicate a double-lock.
func TestNodeLatchNoDoubleLockAcrossEviction(t *testing.T) {
	store := newLatchTestStore(t)

	const nodeIDs = 4
	const goroutines = 40
	const itersPerGoroutine = 500

	counters := make([]int, nodeIDs)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < itersPerGoroutine; i++ {
				id := uint64(g%nodeIDs) + 1
				store.Lock(id)
				// Deliberately unprotected read-increment-write: if two
				// goroutines were ever able to hold the "same" node's latch
				// simultaneously (e.g. one on a stale, evicted object and
				// another on a freshly-created replacement), this would
				// either race (caught by -race) or lose increments (caught
				// by the final count check below).
				counters[id-1]++
				store.Unlock(id)
			}
		}(g)
	}
	wg.Wait()

	perID := goroutines / nodeIDs * itersPerGoroutine
	for id := 0; id < nodeIDs; id++ {
		if counters[id] != perID {
			t.Fatalf("node %d: counter = %d, want %d (mismatch indicates a lost update, i.e. two goroutines held the latch for the same node ID simultaneously)", id+1, counters[id], perID)
		}
	}
}

// TestNodeLatchTryLockFailureDoesNotLeakReference exercises TryLock's failure
// path specifically: a failed TryLock must release its acquireLatch reference
// immediately (not leave it pinned forever), so contended-but-never-succeeding
// node IDs still get reclaimed once the actual holder releases its own lock.
func TestNodeLatchTryLockFailureDoesNotLeakReference(t *testing.T) {
	store := newLatchTestStore(t)
	const nodeID = 1

	store.Lock(nodeID)

	for i := 0; i < 1000; i++ {
		if store.TryLock(nodeID) {
			t.Fatalf("TryLock(%d) unexpectedly succeeded while already held", nodeID)
		}
	}

	store.Unlock(nodeID)

	if got := store.latchRegistrySize(); got != 0 {
		t.Fatalf("latchRegistrySize() = %d after releasing the only held/never-written node, want 0 (failed TryLock attempts must not leak outstanding references)", got)
	}
}
