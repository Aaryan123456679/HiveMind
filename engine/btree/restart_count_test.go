package btree

import (
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// TestRestartFromRootCountIncrementsOnForcedRestart is a permanent regression
// test for the observability counter added alongside pending.md's "consider
// an optional attempt counter/metric (not a hard cap) to make pathological
// retry storms observable" recommendation (surfaced during task-2a.4.2
// verification).
//
// It reuses TestCrabbingConcurrentPropagateNoDeadlock's (insert_test.go)
// hook-based forced-restart pattern: a goroutine holds a child node's latch
// directly, and a concurrent tree.Insert is forced to TryLock-miss on that
// exact node and restart its whole walk from the root (errRestartFromRoot),
// deterministically instead of relying on random concurrency. The test
// asserts RestartFromRootCount() is strictly greater after the forced
// restart than before it, confirming the counter is wired up to a real
// restart-from-root event and not just incremented in dead code.
func TestRestartFromRootCountIncrementsOnForcedRestart(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const prebuilt = 250 // forces at least one leaf split into an internal root (see testInsertLeafSplit)
	rootID, inserted := insertN(t, store, alloc, prebuilt)
	tree := NewTree(store, alloc, rootID)

	isLeaf, _, rootInternal, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if isLeaf || len(rootInternal.Children) < 2 {
		t.Fatalf("prebuilt tree root is not an internal node with >= 2 children (isLeaf=%v children=%v); test setup assumption broken", isLeaf, rootInternal.Children)
	}

	// Mirror crabInsert's own routing logic exactly so contendedChildID is
	// guaranteed to be the node the Insert below will actually try to
	// TryLock as its very next hand-over-hand step after the (uncontended)
	// root lock.
	newKey := genKey(prebuilt)
	childIdx := sort.Search(len(rootInternal.Keys), func(i int) bool { return newKey < rootInternal.Keys[i] })
	contendedChildID := rootInternal.Children[childIdx]

	// Simulate another goroutine already holding contendedChildID's latch by
	// locking it directly here, before starting the Insert we want to force
	// into a restart-from-root.
	store.Lock(contendedChildID)

	before := RestartFromRootCount()

	var restarts int32
	prevHook := crabRetryHook
	crabRetryHook = func(nodeID uint64) {
		if nodeID == contendedChildID {
			atomic.AddInt32(&restarts, 1)
		}
	}
	t.Cleanup(func() { crabRetryHook = prevHook })

	release := make(chan struct{})
	go func() {
		<-release
		store.Unlock(contendedChildID)
	}()

	done := make(chan error, 1)
	go func() { done <- tree.Insert(newKey, uint64(prebuilt+1)) }()

	// Wait for the Insert goroutine to actually hit the contended latch and
	// register at least one restart-from-root before releasing it. Bounded
	// so this test can never hang.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&restarts) == 0 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("Insert never hit the contended latch on node %d within 2s; test setup did not exercise the intended interleaving", contendedChildID)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Insert: unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Insert did not complete within 5s after the contended latch was released")
	}

	if atomic.LoadInt32(&restarts) == 0 {
		t.Fatal("crabRetryHook was never invoked for the contended node; test did not actually exercise the restart-from-root path")
	}

	after := RestartFromRootCount()
	if after <= before {
		t.Fatalf("RestartFromRootCount() did not increase across a forced restart-from-root: before=%d after=%d", before, after)
	}

	inserted[newKey] = uint64(prebuilt + 1)
	finalRoot := tree.Root()
	assertAllLookupable(t, store, finalRoot, inserted)
	assertStructuralInvariants(t, store, finalRoot, len(inserted))
}
