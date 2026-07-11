package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCatalogFileManager exercises the full test spec for subtask 1.1.3: create a
// fresh catalog file, allocate N pages, delete (free) some, verify the free-list
// reclaims/reuses page slots on next allocation, and verify durability across
// reopen.
func TestCatalogFileManager(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.dat")

	t.Run("creates and initializes free-list", func(t *testing.T) {
		fm, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm.Close()

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat(%q) failed after Open: %v", path, err)
		}
		if info.Size() < PageSize {
			t.Fatalf("catalog file size = %d; want >= %d (at least one page)", info.Size(), PageSize)
		}
		if info.Size()%PageSize != 0 {
			t.Fatalf("catalog file size = %d; want a multiple of PageSize (%d)", info.Size(), PageSize)
		}

		if fm.highestAllocated != 0 {
			t.Fatalf("freshly created FileManager.highestAllocated = %d; want 0", fm.highestAllocated)
		}

		// No page should be reported allocatable-as-already-used yet: attempting to
		// free page 1 (which does not exist yet) must fail.
		if err := fm.FreePage(1); err == nil {
			t.Fatalf("FreePage(1) on a freshly initialized free-list = nil error; want error (page 1 does not exist yet)")
		}
	})

	var allocated []uint64

	t.Run("allocates N distinct pages", func(t *testing.T) {
		fm, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm.Close()

		const n = 10
		seen := make(map[uint64]bool, n)
		for i := 0; i < n; i++ {
			id, err := fm.AllocatePage()
			if err != nil {
				t.Fatalf("AllocatePage() #%d = _, %v; want nil error", i, err)
			}
			if id == freeListPageID {
				t.Fatalf("AllocatePage() #%d returned reserved free-list page ID %d", i, id)
			}
			if seen[id] {
				t.Fatalf("AllocatePage() #%d returned duplicate page ID %d", i, id)
			}
			seen[id] = true
			allocated = append(allocated, id)

			// Each allocated page must be immediately usable via ReadPage/WritePage,
			// confirming the free-list marked it used (i.e. a real, addressable page).
			p := NewPage()
			if _, err := p.InsertSlot([]byte("hello")); err != nil {
				t.Fatalf("InsertSlot on newly allocated page %d: %v", id, err)
			}
			if err := fm.WritePage(id, p); err != nil {
				t.Fatalf("WritePage(%d, ...) = %v; want nil error", id, err)
			}
			got, err := fm.ReadPage(id)
			if err != nil {
				t.Fatalf("ReadPage(%d) = _, %v; want nil error", id, err)
			}
			data, err := got.ReadSlot(0)
			if err != nil {
				t.Fatalf("ReadSlot(0) on reloaded page %d: %v", id, err)
			}
			if string(data) != "hello" {
				t.Fatalf("reloaded page %d slot 0 = %q; want %q", id, data, "hello")
			}
		}

		if len(allocated) != n {
			t.Fatalf("allocated %d pages; want %d", len(allocated), n)
		}
	})

	var freed []uint64

	t.Run("frees pages back to the free-list", func(t *testing.T) {
		fm, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm.Close()

		// Simulate "delete/merge" by freeing 3 of the previously allocated pages.
		freed = []uint64{allocated[1], allocated[4], allocated[7]}
		for _, id := range freed {
			if err := fm.FreePage(id); err != nil {
				t.Fatalf("FreePage(%d) = %v; want nil error", id, err)
			}
		}

		// Freeing an already-free / never-allocated page must error.
		if err := fm.FreePage(freeListPageID); err == nil {
			t.Fatalf("FreePage(freeListPageID) = nil error; want error (reserved page)")
		}
		if err := fm.FreePage(fm.highestAllocated + 1); err == nil {
			t.Fatalf("FreePage(%d) on a never-allocated page = nil error; want error", fm.highestAllocated+1)
		}
	})

	t.Run("reuses freed pages on next allocation", func(t *testing.T) {
		fm, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm.Close()

		highBefore := fm.highestAllocated

		reused := make(map[uint64]bool, len(freed))
		for i := 0; i < len(freed); i++ {
			id, err := fm.AllocatePage()
			if err != nil {
				t.Fatalf("AllocatePage() reuse #%d = _, %v; want nil error", i, err)
			}
			reused[id] = true
		}

		wantFreed := make(map[uint64]bool, len(freed))
		for _, id := range freed {
			wantFreed[id] = true
		}
		for id := range reused {
			if !wantFreed[id] {
				t.Fatalf("AllocatePage() reused unexpected page ID %d; want one of the freed set %v", id, freed)
			}
		}
		if len(reused) != len(freed) {
			t.Fatalf("reused %d distinct page IDs; want %d (== number freed)", len(reused), len(freed))
		}

		if fm.highestAllocated != highBefore {
			t.Fatalf("highestAllocated grew from %d to %d while reusing freed pages; want unchanged (reclaim before extend)", highBefore, fm.highestAllocated)
		}
	})

	t.Run("persists free-list across reopen", func(t *testing.T) {
		fm1, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}

		highBefore := fm1.highestAllocated

		// Free one more page, then close, forcing any in-memory-only state to be
		// discarded; only durably persisted state should survive.
		victim := allocated[9]
		if err := fm1.FreePage(victim); err != nil {
			t.Fatalf("FreePage(%d) = %v; want nil error", victim, err)
		}
		if err := fm1.Close(); err != nil {
			t.Fatalf("Close() = %v; want nil error", err)
		}

		fm2, err := Open(path)
		if err != nil {
			t.Fatalf("re-Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm2.Close()

		if fm2.highestAllocated != highBefore {
			t.Fatalf("reopened FileManager.highestAllocated = %d; want %d (persisted high-water mark)", fm2.highestAllocated, highBefore)
		}

		// The freed victim page must be reused before any new page is appended,
		// proving the free bit was durably persisted (not reconstructed empty).
		id, err := fm2.AllocatePage()
		if err != nil {
			t.Fatalf("AllocatePage() after reopen = _, %v; want nil error", err)
		}
		if id != victim {
			t.Fatalf("AllocatePage() after reopen = %d; want reused freed page %d (proves durability)", id, victim)
		}
		if fm2.highestAllocated != highBefore {
			t.Fatalf("highestAllocated after reopen-reuse = %d; want unchanged %d", fm2.highestAllocated, highBefore)
		}

		// A page that was still in-use at close time must not be handed out again.
		stillUsed := allocated[0]
		if err := fm2.FreePage(stillUsed); err != nil {
			t.Fatalf("FreePage(%d) on a page believed still-in-use after reopen failed: %v; the reopened free-list state is inconsistent", stillUsed, err)
		}
	})
}

// TestCatalogFileManagerNarrowLockDoesNotSerializeAcrossIO is the contention-proof
// test for subtask 1.1.5's fix: it demonstrates that FileManager's internal narrow
// lock (the unexported mu field in file.go, guarding only highestAllocated/bitmap
// bookkeeping) is NOT held for the duration of ReadPage/WritePage's actual disk I/O.
//
// Before the fix, catalog.go held a single caller-side fmMu sync.Mutex around every
// FileManager call, including WritePage's synchronous WriteAt+Sync — meaning an
// AllocatePage (or ReadPage/WritePage on a completely unrelated page) would have to
// wait for another in-flight WritePage's full I/O duration to complete before it
// could even begin. This test uses the writeDelay test hook (file.go) to simulate a
// slow WritePage and proves that:
//
//  1. AllocatePage, which needs FileManager's narrow internal lock, completes quickly
//     even while an unrelated page's WritePage call is stuck mid-I/O.
//  2. ReadPage on a different, already-allocated page also completes quickly under
//     the same conditions.
//
// If FileManager's internal lock were (incorrectly) held around the I/O instead of
// just the bookkeeping check/mutation, both assertions below would time out.
func TestCatalogFileManagerNarrowLockDoesNotSerializeAcrossIO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.dat")

	fm, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
	}
	defer fm.Close()

	pageA, err := fm.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage() for pageA: %v", err)
	}
	pageB, err := fm.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage() for pageB: %v", err)
	}

	seedA := NewPage()
	if _, err := seedA.InsertSlot([]byte("seed-a")); err != nil {
		t.Fatalf("seeding pageA: %v", err)
	}
	if err := fm.WritePage(pageA, seedA); err != nil {
		t.Fatalf("WritePage(pageA) seed: %v", err)
	}
	seedB := NewPage()
	if _, err := seedB.InsertSlot([]byte("seed-b")); err != nil {
		t.Fatalf("seeding pageB: %v", err)
	}
	if err := fm.WritePage(pageB, seedB); err != nil {
		t.Fatalf("WritePage(pageB) seed: %v", err)
	}

	// Install a hook that blocks WritePage's I/O (after its brief mu-guarded
	// validDataPageID check has already released mu) until release is closed,
	// simulating a slow fsync without holding fm.mu.
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	fm.writeDelay = func() {
		once.Do(func() { close(entered) })
		<-release
	}

	writeDone := make(chan error, 1)
	go func() {
		slowPage := NewPage()
		if _, err := slowPage.InsertSlot([]byte("slow-write")); err != nil {
			writeDone <- err
			return
		}
		writeDone <- fm.WritePage(pageA, slowPage)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("test setup bug: WritePage(pageA) never reached the writeDelay hook")
	}

	// Assertion 1: AllocatePage (needs FileManager's narrow internal lock) must not
	// block behind pageA's in-flight, artificially slow WritePage.
	allocDone := make(chan error, 1)
	go func() {
		_, err := fm.AllocatePage()
		allocDone <- err
	}()
	select {
	case err := <-allocDone:
		if err != nil {
			t.Fatalf("AllocatePage() while pageA's WritePage was mid-I/O: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AllocatePage blocked behind an unrelated page's in-flight WritePage I/O — the internal lock is too broad")
	}

	// Assertion 2: ReadPage on a different, already-allocated page must also proceed
	// without waiting for pageA's slow WritePage to finish.
	readDone := make(chan error, 1)
	go func() {
		_, err := fm.ReadPage(pageB)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("ReadPage(pageB) while pageA's WritePage was mid-I/O: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadPage(pageB) blocked behind an unrelated page's in-flight WritePage I/O — the internal lock is too broad")
	}

	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("delayed WritePage(pageA) = %v; want nil error", err)
	}
}

// TestFreePageDoubleFreeRejected exercises the test spec for subtask 4.5.5.1: free a
// page, then free the same page again, and assert the second call returns an
// explicit error instead of silently succeeding.
func TestFreePageDoubleFreeRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.dat")

	fm, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
	}
	defer fm.Close()

	id, err := fm.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage() = _, %v; want nil error", err)
	}

	if err := fm.FreePage(id); err != nil {
		t.Fatalf("first FreePage(%d) = %v; want nil error", id, err)
	}

	if err := fm.FreePage(id); err == nil {
		t.Fatalf("second FreePage(%d) (double-free) = nil error; want an explicit error", id)
	}
}

// TestFreeListCapacityExhaustionSurfacesError exercises the test spec for subtask
// 4.5.13.2 (issue #51): once the free-list bitmap's hard capacity ceiling
// (bitmapCapacityBits pages, ~32704 pages / ~128MB per the doc comment on
// bitmapCapacityBits) is reached with no freed page available for reuse,
// AllocatePage must surface an explicit, documented error rather than an ambiguous
// failure (e.g. a bounds panic, a corrupted bitmap write, or silently returning a
// bogus page ID).
//
// This test drives FileManager's in-memory bookkeeping directly to
// highestAllocated == bitmapCapacityBits with every bit marked used, instead of
// actually performing bitmapCapacityBits real AllocatePage calls (each of which
// would extend the file by one page and fsync twice); the latter is behaviorally
// equivalent for exercising this specific exhaustion check but would make this test
// prohibitively slow (~32.7k pages worth of file-extension + fsync I/O) without
// adding any additional coverage of the exhaustion branch itself.
func TestFreeListCapacityExhaustionSurfacesError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.dat")

	fm, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
	}
	defer fm.Close()

	// Simulate the free-list already being at full capacity: every representable
	// page ID has been allocated and none has been freed, so AllocatePage's
	// free-page scan will find nothing to reuse and must fall through to the
	// extend-the-file path, which is where the capacity ceiling is enforced.
	fm.highestAllocated = bitmapCapacityBits
	for id := uint64(1); id <= bitmapCapacityBits; id++ {
		fm.setUsed(id, true)
	}

	id, err := fm.AllocatePage()
	if err == nil {
		t.Fatalf("AllocatePage() at capacity = %d, nil error; want an explicit free-list-exhausted error", id)
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("AllocatePage() at capacity error = %q; want it to explicitly mention free-list exhaustion", err.Error())
	}
}
