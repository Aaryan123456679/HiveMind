package btree

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// buildTestTree is TEST-ONLY SCAFFOLDING for exercising Lookup. It is NOT subtask
// 1.2.3's real insert-with-splitting API: it hand-constructs a fixed, already-
// balanced tree shape by directly assembling LeafNode/InternalNode values and
// writing them to disk via NodeStore. It performs no splitting, no rebalancing, and
// no general-purpose insert logic -- it exists solely because 1.2.3 (real insert)
// has not landed yet and Lookup needs *some* on-disk tree to traverse. Do not reuse
// this as, or mistake it for, the real insert implementation.
//
// Tree shape (3 levels):
//
//	root (internal, node ID 7): Keys=["billing/invoice"], Children=[internal1, internal2]
//	  internal1 (node ID 5): Keys=["auth/oauth"],     Children=[leaf1, leaf2]
//	    leaf1 (node ID 1): "auth/login"=101, "auth/logout"=102        -> NextLeaf leaf2
//	    leaf2 (node ID 2): "auth/oauth"=103, "auth/session"=104       -> NextLeaf leaf3
//	  internal2 (node ID 6): Keys=["search/index"],    Children=[leaf3, leaf4]
//	    leaf3 (node ID 3): "billing/invoice"=201, "billing/plan"=202  -> NextLeaf leaf4
//	    leaf4 (node ID 4): "search/index"=301, "search/query"=302    -> NextLeaf 0 (none)
//
// Node IDs are assigned arbitrarily (leaves 1-4, internals 5-6, root 7); only that
// every ID is >= 1 (0 is reserved) and consistent with the Children/NextLeaf
// pointers matters.
func buildTestTree(t *testing.T) (store *NodeStore, rootID uint64) {
	t.Helper()

	const (
		leaf1ID = uint64(1)
		leaf2ID = uint64(2)
		leaf3ID = uint64(3)
		leaf4ID = uint64(4)
		int1ID  = uint64(5)
		int2ID  = uint64(6)
		rootID_ = uint64(7)
	)

	path := filepath.Join(t.TempDir(), "name.idx")
	f, err := OpenIndexFile(path)
	if err != nil {
		t.Fatalf("OpenIndexFile: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	store = NewNodeStore(f)

	nodes := []struct {
		id       uint64
		leaf     *LeafNode
		internal *InternalNode
	}{
		{id: leaf1ID, leaf: &LeafNode{
			Keys: []string{"auth/login", "auth/logout"}, FileIDs: []uint64{101, 102}, NextLeaf: leaf2ID,
		}},
		{id: leaf2ID, leaf: &LeafNode{
			Keys: []string{"auth/oauth", "auth/session"}, FileIDs: []uint64{103, 104}, NextLeaf: leaf3ID,
		}},
		{id: leaf3ID, leaf: &LeafNode{
			Keys: []string{"billing/invoice", "billing/plan"}, FileIDs: []uint64{201, 202}, NextLeaf: leaf4ID,
		}},
		{id: leaf4ID, leaf: &LeafNode{
			Keys: []string{"search/index", "search/query"}, FileIDs: []uint64{301, 302}, NextLeaf: noSibling,
		}},
		{id: int1ID, internal: &InternalNode{
			Keys: []string{"auth/oauth"}, Children: []uint64{leaf1ID, leaf2ID},
		}},
		{id: int2ID, internal: &InternalNode{
			Keys: []string{"search/index"}, Children: []uint64{leaf3ID, leaf4ID},
		}},
		{id: rootID_, internal: &InternalNode{
			Keys: []string{"billing/invoice"}, Children: []uint64{int1ID, int2ID},
		}},
	}

	for _, n := range nodes {
		var encoded []byte
		var err error
		if n.leaf != nil {
			encoded, err = n.leaf.Encode()
		} else {
			encoded, err = n.internal.Encode()
		}
		if err != nil {
			t.Fatalf("encoding node %d: %v", n.id, err)
		}
		if err := store.WriteNode(n.id, encoded); err != nil {
			t.Fatalf("writing node %d: %v", n.id, err)
		}
	}

	return store, rootID_
}

func TestLookup(t *testing.T) {
	store, rootID := buildTestTree(t)

	t.Run("present", func(t *testing.T) {
		cases := []struct {
			path       string
			wantFileID uint64
		}{
			{"auth/login", 101},
			{"auth/logout", 102},
			{"auth/oauth", 103},
			{"auth/session", 104},
			{"billing/invoice", 201},
			{"billing/plan", 202},
			{"search/index", 301},
			{"search/query", 302},
		}
		for _, tc := range cases {
			fileID, found, err := Lookup(store, rootID, tc.path)
			if err != nil {
				t.Fatalf("Lookup(%q): unexpected error: %v", tc.path, err)
			}
			if !found {
				t.Fatalf("Lookup(%q): expected found=true, got false", tc.path)
			}
			if fileID != tc.wantFileID {
				t.Fatalf("Lookup(%q): fileID = %d, want %d", tc.path, fileID, tc.wantFileID)
			}
		}
	})

	t.Run("absent", func(t *testing.T) {
		// These paths sort into leaves that DO have other real keys, proving
		// Lookup genuinely checks for an exact key match rather than just
		// treating "found a leaf" as success.
		cases := []string{
			"auth/middleware", // between auth/logout and auth/oauth
			"billing/refund",  // after billing/plan, within billing's range
		}
		for _, path := range cases {
			fileID, found, err := Lookup(store, rootID, path)
			if err != nil {
				t.Fatalf("Lookup(%q): unexpected error: %v", path, err)
			}
			if found {
				t.Fatalf("Lookup(%q): expected found=false, got true (fileID=%d)", path, fileID)
			}
			if fileID != 0 {
				t.Fatalf("Lookup(%q): expected fileID=0 on not-found, got %d", path, fileID)
			}
		}
	})

	t.Run("boundary", func(t *testing.T) {
		cases := []string{
			"aaa/first", // sorts before every key in the whole tree
			"zzz/last",  // sorts after every key in the whole tree
		}
		for _, path := range cases {
			fileID, found, err := Lookup(store, rootID, path)
			if err != nil {
				t.Fatalf("Lookup(%q): unexpected error: %v", path, err)
			}
			if found {
				t.Fatalf("Lookup(%q): expected found=false, got true (fileID=%d)", path, fileID)
			}
			if fileID != 0 {
				t.Fatalf("Lookup(%q): expected fileID=0 on not-found, got %d", path, fileID)
			}
		}
	})
}

// TestLookupInternalNodeMultiKeyRouting is subtask 4.5.12.3's (issue #50)
// required test spec. descendToLeaf/lookupOnce route through an internal
// node via `i := sort.Search(len(internal.Keys), ...); currentID =
// internal.Children[i]`; with an internal node that has only a single key
// (as buildTestTree's fixture above exclusively uses), i can only ever be 0
// (path < Keys[0]) or 1 == len(Keys) (path >= Keys[0]) -- the "route into a
// strictly-interior child" case, 0 < i < len(Keys), which requires an
// internal node with at least 2 keys (hence >= 3 children), is never
// exercised by any existing test. This test hand-constructs exactly such a
// node -- 2 keys, 3 children -- and looks up a path that sorts strictly
// between Keys[0] and Keys[1], forcing i == 1 (0 < 1 < 2), i.e. routing into
// the MIDDLE child, neither the leftmost (i==0) nor rightmost (i==len(Keys))
// one. It exercises this both via the pre-existing single-threaded free
// function Lookup (descendToLeaf) and via the concurrency-safe Tree.Lookup
// (lookupOnce), since both implement this same routing shape independently.
func TestLookupInternalNodeMultiKeyRouting(t *testing.T) {
	const (
		leafAID = uint64(1) // covers [-inf, "fruit/banana")
		leafBID = uint64(2) // covers ["fruit/banana", "fruit/kiwi") -- the middle child
		leafCID = uint64(3) // covers ["fruit/kiwi", +inf)
		rootID  = uint64(4)
	)

	path := filepath.Join(t.TempDir(), "name.idx")
	f, err := OpenIndexFile(path)
	if err != nil {
		t.Fatalf("OpenIndexFile: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	store := NewNodeStore(f)

	nodes := []struct {
		id       uint64
		leaf     *LeafNode
		internal *InternalNode
	}{
		{id: leafAID, leaf: &LeafNode{
			Keys: []string{"fruit/apple"}, FileIDs: []uint64{1}, NextLeaf: leafBID,
		}},
		{id: leafBID, leaf: &LeafNode{
			Keys: []string{"fruit/banana", "fruit/grape"}, FileIDs: []uint64{2, 3}, NextLeaf: leafCID,
		}},
		{id: leafCID, leaf: &LeafNode{
			Keys: []string{"fruit/kiwi", "fruit/mango"}, FileIDs: []uint64{4, 5}, NextLeaf: noSibling,
		}},
		// The node under test: 2 keys, 3 children. sort.Search over
		// ["fruit/banana", "fruit/kiwi"] returns:
		//   i=0 for path < "fruit/banana"          -> Children[0] (leafAID)
		//   i=1 for "fruit/banana" <= path < "fruit/kiwi" -> Children[1] (leafBID, MIDDLE)
		//   i=2 for path >= "fruit/kiwi"            -> Children[2] (leafCID)
		{id: rootID, internal: &InternalNode{
			Keys:     []string{"fruit/banana", "fruit/kiwi"},
			Children: []uint64{leafAID, leafBID, leafCID},
		}},
	}

	for _, n := range nodes {
		var encoded []byte
		var err error
		if n.leaf != nil {
			encoded, err = n.leaf.Encode()
		} else {
			encoded, err = n.internal.Encode()
		}
		if err != nil {
			t.Fatalf("encoding node %d: %v", n.id, err)
		}
		if err := store.WriteNode(n.id, encoded); err != nil {
			t.Fatalf("writing node %d: %v", n.id, err)
		}
	}

	// middlePaths sort strictly between Keys[0] ("fruit/banana") and
	// Keys[1] ("fruit/kiwi"), so i == 1 -- 0 < i < len(Keys) -- and descent
	// must land on leafBID, the middle child. If the routing branch were
	// broken (e.g. always picking Children[0] or Children[len(Keys)]),
	// these would resolve to leafAID/leafCID instead and either return the
	// wrong fileID or a false not-found.
	middleCases := []struct {
		path       string
		wantFileID uint64
	}{
		{"fruit/banana", 2}, // == Keys[0]: sort.Search's f(0) is false, f(1) is true, so i=1
		{"fruit/grape", 3},  // strictly between Keys[0] and Keys[1]: i=1
	}

	// leftCases/rightCases pin down that the OTHER branches (i==0,
	// i==len(Keys)) still route correctly on this exact multi-key node, so
	// this test also confirms the middle-child case isn't accidentally
	// passing due to some other routing bug that happens to also land on
	// leafBID.
	leftCases := []struct {
		path       string
		wantFileID uint64
	}{
		{"fruit/apple", 1},
	}
	rightCases := []struct {
		path       string
		wantFileID uint64
	}{
		{"fruit/kiwi", 4},
		{"fruit/mango", 5},
	}

	checkPresent := func(t *testing.T, path string, wantFileID uint64) {
		t.Helper()
		fileID, found, err := Lookup(store, rootID, path)
		if err != nil {
			t.Fatalf("Lookup(%q): unexpected error: %v", path, err)
		}
		if !found || fileID != wantFileID {
			t.Fatalf("Lookup(%q): found=%v fileID=%d, want found=true fileID=%d", path, found, fileID, wantFileID)
		}

		alloc, err := NewNodeAllocator(store)
		if err != nil {
			t.Fatalf("NewNodeAllocator: %v", err)
		}
		t.Cleanup(func() { alloc.Close() })
		tree := NewTree(store, alloc, rootID)
		fileID, found, err = tree.Lookup(path)
		if err != nil {
			t.Fatalf("Tree.Lookup(%q): unexpected error: %v", path, err)
		}
		if !found || fileID != wantFileID {
			t.Fatalf("Tree.Lookup(%q): found=%v fileID=%d, want found=true fileID=%d", path, found, fileID, wantFileID)
		}
	}

	t.Run("middle child (0 < i < len(Keys))", func(t *testing.T) {
		for _, tc := range middleCases {
			checkPresent(t, tc.path, tc.wantFileID)
		}
	})
	t.Run("leftmost child (i == 0)", func(t *testing.T) {
		for _, tc := range leftCases {
			checkPresent(t, tc.path, tc.wantFileID)
		}
	})
	t.Run("rightmost child (i == len(Keys))", func(t *testing.T) {
		for _, tc := range rightCases {
			checkPresent(t, tc.path, tc.wantFileID)
		}
	})
}

// TestOptimisticRead is task-2a.4.4's acceptance test: Tree.Lookup (the new
// lock-free, optimistic-version-counter read path) must never block a
// concurrent writer or be blocked by one, and must never return
// corrupted/stale data when it races a concurrent structural mutation --
// either the value is consistent with some real point-in-time state, or the
// read detects the conflict via the node's version counter and retries.
func TestOptimisticRead(t *testing.T) {
	t.Run("NoConcurrency", testOptimisticReadNoConcurrency)
	t.Run("InterleavedWithInsertDelete", testOptimisticReadInterleavedWithInsertDelete)
	t.Run("ForcedRetryDeterministic", testOptimisticReadForcedRetryDeterministic)
}

// testOptimisticReadNoConcurrency is the sanity baseline: with zero
// concurrency, Tree.Lookup must agree exactly with the pre-existing Phase-1
// free function Lookup for every present and a handful of absent keys, over
// a genuinely multi-level tree (insertN forces splits), proving the
// Blink-tree move-right descent logic in lookupOnce is not itself broken
// even before any racing writer is involved.
func testOptimisticReadNoConcurrency(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 500 // forces multiple leaf splits and an internal root
	rootID, inserted := insertN(t, store, alloc, n)
	tree := NewTree(store, alloc, rootID)

	for key, wantFileID := range inserted {
		fileID, found, err := tree.Lookup(key)
		if err != nil {
			t.Fatalf("Tree.Lookup(%q): unexpected error: %v", key, err)
		}
		if !found {
			t.Fatalf("Tree.Lookup(%q): expected found=true, got false", key)
		}
		if fileID != wantFileID {
			t.Fatalf("Tree.Lookup(%q): fileID = %d, want %d", key, fileID, wantFileID)
		}

		// Cross-check against the pre-existing single-threaded Lookup to
		// confirm Tree.Lookup did not change externally observable
		// semantics for the present-key case.
		wantFileID2, wantFound2, err := Lookup(store, rootID, key)
		if err != nil || !wantFound2 || wantFileID2 != wantFileID {
			t.Fatalf("Lookup(%q) disagrees with test setup: fileID=%d found=%v err=%v", key, wantFileID2, wantFound2, err)
		}
	}

	absentCases := []string{"aaa/before-everything", "zzz/after-everything", "topic0000/pag"}
	for _, key := range absentCases {
		fileID, found, err := tree.Lookup(key)
		if err != nil {
			t.Fatalf("Tree.Lookup(%q): unexpected error: %v", key, err)
		}
		if found {
			t.Fatalf("Tree.Lookup(%q): expected found=false, got true (fileID=%d)", key, fileID)
		}
		if fileID != 0 {
			t.Fatalf("Tree.Lookup(%q): expected fileID=0 on not-found, got %d", key, fileID)
		}
	}
}

// testOptimisticReadInterleavedWithInsertDelete is this subtask's core
// concurrency acceptance test, mirroring delete_test.go's
// testCrabbingDeleteInterleavedWithInsert oracle style: concurrent
// Tree.Lookup goroutines run continuously alongside concurrent
// Tree.Insert/Tree.Delete goroutines touching overlapping key ranges (real
// splits/merges/propagation in flight, not just far-apart disjoint
// subtrees), all under -race. Every single Tree.Lookup result is checked
// against an oracle of what is a POSSIBLE point-in-time answer for that key;
// no result may ever indicate corruption, and untouched keys must be found
// with their exact original fileID on every single lookup (they are never
// mutated, so any transient "not found" for one of them would itself prove
// the optimistic read path returned stale/torn data instead of correctly
// retrying).
func testOptimisticReadInterleavedWithInsertDelete(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	const n = 4000
	rootID, inserted := insertN(t, store, alloc, n)
	tree := NewTree(store, alloc, rootID)

	newKey := func(i int) string { return genKey(i) + "-new" }

	var toDelete []string  // i%3 == 0: deleted concurrently, never reinserted
	var toInsertIdx []int  // i%3 == 1: newKey(i) concurrently inserted
	var untouched []string // i%3 == 2: never touched; must always be found
	untouchedFileID := make(map[string]uint64, n/3+1)
	for i := 0; i < n; i++ {
		switch i % 3 {
		case 0:
			toDelete = append(toDelete, genKey(i))
		case 1:
			toInsertIdx = append(toInsertIdx, i)
		case 2:
			key := genKey(i)
			untouched = append(untouched, key)
			untouchedFileID[key] = inserted[key]
		}
	}

	const delGoroutines = 8
	const insGoroutines = 8
	const lookupGoroutines = 8

	var writersWG sync.WaitGroup
	errCh := make(chan error, delGoroutines+insGoroutines+lookupGoroutines)
	stopReaders := make(chan struct{})

	for g := 0; g < delGoroutines; g++ {
		g := g
		writersWG.Add(1)
		go func() {
			defer writersWG.Done()
			for idx := g; idx < len(toDelete); idx += delGoroutines {
				key := toDelete[idx]
				found, err := tree.Delete(key)
				if err != nil {
					errCh <- fmt.Errorf("delete goroutine %d: Delete(%q): %w", g, key, err)
					return
				}
				if !found {
					errCh <- fmt.Errorf("delete goroutine %d: Delete(%q): expected found=true, got false", g, key)
					return
				}
			}
		}()
	}
	for g := 0; g < insGoroutines; g++ {
		g := g
		writersWG.Add(1)
		go func() {
			defer writersWG.Done()
			for idx := g; idx < len(toInsertIdx); idx += insGoroutines {
				i := toInsertIdx[idx]
				key := newKey(i)
				fileID := uint64(1_000_000 + i)
				if err := tree.Insert(key, fileID); err != nil {
					errCh <- fmt.Errorf("insert goroutine %d: Insert(%q): %w", g, key, err)
					return
				}
			}
		}()
	}

	var readersWG sync.WaitGroup
	for g := 0; g < lookupGoroutines; g++ {
		g := g
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			iter := 0
			for {
				select {
				case <-stopReaders:
					return
				default:
				}
				iter++

				// Untouched key: must ALWAYS be found with its exact
				// original fileID -- the strongest possible check, since
				// this key is never mutated by any writer goroutine.
				uKey := untouched[(g+iter)%len(untouched)]
				fileID, found, err := tree.Lookup(uKey)
				if err != nil {
					errCh <- fmt.Errorf("lookup goroutine %d: Tree.Lookup(%q) [untouched]: %w", g, uKey, err)
					return
				}
				if !found || fileID != untouchedFileID[uKey] {
					errCh <- fmt.Errorf("lookup goroutine %d: Tree.Lookup(%q) [untouched]: found=%v fileID=%d, want found=true fileID=%d", g, uKey, found, fileID, untouchedFileID[uKey])
					return
				}

				// Delete-in-flight key: either still found with its
				// original fileID (delete hasn't landed yet) or correctly
				// absent (delete has landed) -- any other fileID would be
				// corruption.
				dKey := toDelete[(g+iter)%len(toDelete)]
				fileID, found, err = tree.Lookup(dKey)
				if err != nil {
					errCh <- fmt.Errorf("lookup goroutine %d: Tree.Lookup(%q) [deleting]: %w", g, dKey, err)
					return
				}
				if found && fileID != inserted[dKey] {
					errCh <- fmt.Errorf("lookup goroutine %d: Tree.Lookup(%q) [deleting]: found stale/corrupt fileID=%d, want %d", g, dKey, fileID, inserted[dKey])
					return
				}

				// Insert-in-flight key: either not yet present, or present
				// with exactly its inserted fileID -- any other fileID
				// would be corruption.
				i := toInsertIdx[(g+iter)%len(toInsertIdx)]
				iKey := newKey(i)
				wantFileID := uint64(1_000_000 + i)
				fileID, found, err = tree.Lookup(iKey)
				if err != nil {
					errCh <- fmt.Errorf("lookup goroutine %d: Tree.Lookup(%q) [inserting]: %w", g, iKey, err)
					return
				}
				if found && fileID != wantFileID {
					errCh <- fmt.Errorf("lookup goroutine %d: Tree.Lookup(%q) [inserting]: found stale/corrupt fileID=%d, want %d", g, iKey, fileID, wantFileID)
					return
				}
			}
		}()
	}

	writersWG.Wait()
	close(stopReaders)
	readersWG.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	finalRoot := tree.Root()

	wantPresent := make(map[string]uint64)
	var wantAbsent []string
	for i := 0; i < n; i++ {
		key := genKey(i)
		if i%3 == 0 {
			wantAbsent = append(wantAbsent, key)
			continue
		}
		wantPresent[key] = inserted[key]
		if i%3 == 1 {
			wantPresent[newKey(i)] = uint64(1_000_000 + i)
		}
	}
	assertAllLookupable(t, store, finalRoot, wantPresent)
	assertAbsent(t, store, finalRoot, wantAbsent)
	assertStructuralInvariants(t, store, finalRoot, len(wantPresent))
	assertNoOrphanedPointers(t, store, finalRoot)

	// Final confirmation that the concurrent Tree.Lookup path itself agrees
	// with the post-mutation ground truth, not just the free Lookup used
	// above by the shared assertion helpers.
	for key, wantFileID := range wantPresent {
		fileID, found, err := tree.Lookup(key)
		if err != nil || !found || fileID != wantFileID {
			t.Fatalf("post-run Tree.Lookup(%q): fileID=%d found=%v err=%v, want fileID=%d found=true", key, fileID, found, err, wantFileID)
		}
	}
	for _, key := range wantAbsent {
		fileID, found, err := tree.Lookup(key)
		if err != nil || found {
			t.Fatalf("post-run Tree.Lookup(%q): expected found=false, got fileID=%d found=%v err=%v", key, fileID, found, err)
		}
	}
}

// testOptimisticReadForcedRetryDeterministic deterministically exercises the
// version-mismatch retry path itself (not just probabilistically), mirroring
// TestCrabbingConcurrentPropagateNoDeadlock's (insert_test.go) hook-based
// synchronization pattern: optimisticReadHook pauses a Tree.Lookup goroutine
// immediately after it has read a node's content but before its confirming
// second Version load, a concurrent goroutine performs a real Tree.Insert
// that writes (and bumps the version of) that exact node while the lookup is
// paused, and only then is the lookup allowed to proceed -- guaranteeing a
// genuine version mismatch and a real retry, observed via
// optimisticRetryHook, rather than a return of torn data.
func testOptimisticReadForcedRetryDeterministic(t *testing.T) {
	store, alloc := newTestStoreAndAllocator(t)

	// Small enough that insertN's root IS the single leaf (no split yet):
	// the node Tree.Lookup reads first is exactly the node the concurrent
	// Insert below will mutate.
	const n = 5
	rootID, inserted := insertN(t, store, alloc, n)
	tree := NewTree(store, alloc, rootID)

	isLeaf, _, _, err := store.ReadNode(rootID)
	if err != nil {
		t.Fatalf("ReadNode(root): unexpected error: %v", err)
	}
	if !isLeaf {
		t.Fatalf("test setup assumption broken: root %d is not a single leaf (n=%d too large)", rootID, n)
	}

	targetKey := genKey(0)
	wantFileID := inserted[targetKey]

	var paused int32
	var retries int32
	prevReadHook, prevRetryHook := optimisticReadHook, optimisticRetryHook
	release := make(chan struct{})
	var once sync.Once
	optimisticReadHook = func(nodeID uint64) {
		if nodeID != rootID {
			return
		}
		// Only pause the very first read of rootID: once the forced
		// mismatch has been triggered, subsequent retries must proceed
		// normally or this test would hang forever instead of failing
		// fast.
		once.Do(func() {
			atomic.StoreInt32(&paused, 1)
			<-release
		})
	}
	optimisticRetryHook = func(nodeID uint64) {
		if nodeID == rootID {
			atomic.AddInt32(&retries, 1)
		}
	}
	t.Cleanup(func() {
		optimisticReadHook = prevReadHook
		optimisticRetryHook = prevRetryHook
	})

	done := make(chan struct {
		fileID uint64
		found  bool
		err    error
	}, 1)
	go func() {
		fileID, found, err := tree.Lookup(targetKey)
		done <- struct {
			fileID uint64
			found  bool
			err    error
		}{fileID, found, err}
	}()

	// Wait for the lookup to actually reach the pause point before mutating
	// the node out from under it. Bounded so a regression (e.g. the pause
	// point never firing) surfaces as a clear failure, not a silent hang.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&paused) == 0 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("Tree.Lookup never reached the forced-pause point on node %d within 2s", rootID)
		}
		time.Sleep(time.Millisecond)
	}

	// Mutate the exact node the paused lookup already read, via the real
	// Insert path (bumps rootID's version through the ordinary WriteNode
	// choke point), then release the paused lookup.
	newKey := genKey(n) // does not exist yet; upsert-free real insert
	newFileID := uint64(9999)
	if err := tree.Insert(newKey, newFileID); err != nil {
		close(release)
		t.Fatalf("Insert(%q): unexpected error: %v", newKey, err)
	}
	close(release)

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Tree.Lookup(%q): unexpected error: %v", targetKey, result.err)
		}
		if !result.found || result.fileID != wantFileID {
			t.Fatalf("Tree.Lookup(%q): found=%v fileID=%d, want found=true fileID=%d", targetKey, result.found, result.fileID, wantFileID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Tree.Lookup did not complete within 5s after the forced version mismatch was released")
	}

	if atomic.LoadInt32(&retries) == 0 {
		t.Fatal("optimisticRetryHook was never invoked for the target node; test did not actually force the version-mismatch retry path")
	}
}

// TestReadWriteNodeErrorPaths is subtask 4.5.1.6's (issue #38) required test
// spec: it locks down three defensive error paths in ReadNode/WriteNode that
// existed already but had no dedicated coverage -- reserved node ID 0,
// an unrecognized node-type discriminator byte, and a wrong-length WriteNode
// buffer. This is test-only: none of these paths' behavior is being changed,
// only exercised and asserted for the first time.
func TestReadWriteNodeErrorPaths(t *testing.T) {
	t.Run("reserved node ID 0", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "name.idx")
		f, err := OpenIndexFile(path)
		if err != nil {
			t.Fatalf("OpenIndexFile: %v", err)
		}
		t.Cleanup(func() { f.Close() })
		store := NewNodeStore(f)

		// Write a real node at a non-reserved ID FIRST, so the underlying
		// file grows past the reserved ID's own byte offset before
		// ReadNode(reservedNodeID) is ever called. This matters: on a
		// still-empty file, ReadAt(reservedNodeID's offset) short-reads and
		// fails regardless of whether ReadNode's own reservedNodeID guard
		// exists, which would make this subtest pass even if that guard
		// were deleted (illusory coverage). Once the file has grown (the
		// realistic on-disk state -- any real index file has other nodes
		// written before node ID 0 would ever be probed), a full NodeSize
		// window of legitimate, all-zero-or-populated bytes is readable at
		// every offset including 0's, so a subsequent ReadAt at offset 0
		// succeeds cleanly; only ReadNode's explicit reservedNodeID check
		// can still catch the reserved-ID access, and without it the
		// all-zero bytes at offset 0 would silently decode as a
		// deceptively "valid" empty leaf (isLeaf=true, err=nil) instead of
		// failing, since nodeTypeLeaf == 0.
		const otherNodeID = uint64(1)
		grownEncoded, err := (LeafNode{Keys: []string{"a"}, FileIDs: []uint64{1}}).Encode()
		if err != nil {
			t.Fatalf("Encode: unexpected error: %v", err)
		}
		if err := store.WriteNode(otherNodeID, grownEncoded); err != nil {
			t.Fatalf("WriteNode(otherNodeID, ...): unexpected error: %v", err)
		}

		isLeaf, leaf, internal, err := store.ReadNode(reservedNodeID)
		if err == nil {
			t.Fatalf("ReadNode(reservedNodeID): expected a non-nil error on a grown file, got nil (isLeaf=%v, leaf=%+v, internal=%+v) -- this would silently decode as a bogus empty leaf without the reservedNodeID guard", isLeaf, leaf, internal)
		}
		if isLeaf {
			t.Fatalf("ReadNode(reservedNodeID): isLeaf = true, want false alongside the error")
		}

		validEncoded, err := (LeafNode{Keys: []string{"a"}, FileIDs: []uint64{1}}).Encode()
		if err != nil {
			t.Fatalf("Encode: unexpected error: %v", err)
		}
		if err := store.WriteNode(reservedNodeID, validEncoded); err == nil {
			t.Fatal("WriteNode(reservedNodeID, ...): expected a non-nil error, got nil")
		}
	})

	t.Run("unrecognized type discriminator", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "name.idx")
		f, err := OpenIndexFile(path)
		if err != nil {
			t.Fatalf("OpenIndexFile: %v", err)
		}
		t.Cleanup(func() { f.Close() })
		store := NewNodeStore(f)

		// Hand-write a raw NodeSize-byte buffer with an invalid type
		// discriminator directly via the underlying file handle: Encode()
		// never produces a discriminator other than nodeTypeLeaf/
		// nodeTypeInternal, so WriteNode's own encode path can't be used to
		// construct this malformed-on-disk scenario (e.g. a partially
		// corrupted node from an unrelated on-disk fault).
		const badNodeID = uint64(1)
		raw := make([]byte, NodeSize)
		raw[offNodeType] = 0xFF // neither nodeTypeLeaf (0) nor nodeTypeInternal (1)
		offset := int64(badNodeID) * int64(NodeSize)
		if _, err := f.WriteAt(raw, offset); err != nil {
			t.Fatalf("WriteAt: unexpected error: %v", err)
		}

		isLeaf, leaf, internal, err := store.ReadNode(badNodeID)
		if err == nil {
			t.Fatalf("ReadNode(badNodeID): expected a non-nil error for an unrecognized type discriminator, got nil (isLeaf=%v, leaf=%+v, internal=%+v)", isLeaf, leaf, internal)
		}
	})

	t.Run("wrong-length write", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "name.idx")
		f, err := OpenIndexFile(path)
		if err != nil {
			t.Fatalf("OpenIndexFile: %v", err)
		}
		t.Cleanup(func() { f.Close() })
		store := NewNodeStore(f)

		const nodeID = uint64(1)
		tooShort := make([]byte, NodeSize-1)
		if err := store.WriteNode(nodeID, tooShort); err == nil {
			t.Fatal("WriteNode with a NodeSize-1-byte buffer: expected a non-nil error, got nil")
		}

		tooLong := make([]byte, NodeSize+1)
		if err := store.WriteNode(nodeID, tooLong); err == nil {
			t.Fatal("WriteNode with a NodeSize+1-byte buffer: expected a non-nil error, got nil")
		}

		// Collateral check: a subsequent, correctly-sized write to a
		// different node ID must still succeed and round-trip cleanly --
		// the rejected wrong-length writes above must not have corrupted
		// the store or left it in a bad state.
		const otherNodeID = uint64(2)
		valid := LeafNode{Keys: []string{"auth/login"}, FileIDs: []uint64{101}}
		encoded, err := valid.Encode()
		if err != nil {
			t.Fatalf("Encode: unexpected error: %v", err)
		}
		if err := store.WriteNode(otherNodeID, encoded); err != nil {
			t.Fatalf("WriteNode(otherNodeID, ...): unexpected error: %v", err)
		}
		isLeaf, leaf, _, err := store.ReadNode(otherNodeID)
		if err != nil {
			t.Fatalf("ReadNode(otherNodeID): unexpected error: %v", err)
		}
		if !isLeaf || len(leaf.Keys) != 1 || leaf.Keys[0] != "auth/login" || leaf.FileIDs[0] != 101 {
			t.Fatalf("ReadNode(otherNodeID) = isLeaf=%v leaf=%+v, want isLeaf=true leaf with key auth/login=101", isLeaf, leaf)
		}
	})
}
