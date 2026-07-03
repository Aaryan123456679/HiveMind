package catalog

import (
	"os"
	"path/filepath"
	"testing"
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
