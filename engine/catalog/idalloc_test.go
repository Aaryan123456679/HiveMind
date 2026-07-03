package catalog

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestFileIDAllocator exercises the full test spec for subtask 1.1.4: sequential
// allocation is strictly increasing from 1, concurrent allocation from many
// goroutines yields exactly that many unique IDs with no duplicates, and the
// high-water-mark durably survives a reopen of the underlying catalog file with no
// collision against previously allocated IDs.
func TestFileIDAllocator(t *testing.T) {
	t.Run("sequential allocation is strictly increasing from 1", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "catalog.dat")

		fm, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm.Close()

		alloc, err := NewIDAllocator(fm)
		if err != nil {
			t.Fatalf("NewIDAllocator(fm) = _, %v; want nil error", err)
		}
		defer alloc.Close()

		const n = 50
		var prev uint64
		for i := 0; i < n; i++ {
			id, err := alloc.Next()
			if err != nil {
				t.Fatalf("Next() #%d = _, %v; want nil error", i, err)
			}
			if i == 0 && id != 1 {
				t.Fatalf("first Next() = %d; want 1 (0 is reserved as InvalidFileID)", id)
			}
			if id != prev+1 {
				t.Fatalf("Next() #%d = %d; want strictly prev+1 (prev=%d)", i, id, prev)
			}
			prev = id
		}
	})

	t.Run("concurrent allocation yields unique IDs, no duplicates", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "catalog.dat")

		fm, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm.Close()

		alloc, err := NewIDAllocator(fm)
		if err != nil {
			t.Fatalf("NewIDAllocator(fm) = _, %v; want nil error", err)
		}
		defer alloc.Close()

		const goroutines = 100
		const perGoroutine = 100
		const total = goroutines * perGoroutine

		results := make(chan uint64, total)
		errs := make(chan error, total)

		var wg sync.WaitGroup
		wg.Add(goroutines)
		for g := 0; g < goroutines; g++ {
			go func() {
				defer wg.Done()
				for i := 0; i < perGoroutine; i++ {
					id, err := alloc.Next()
					if err != nil {
						errs <- err
						return
					}
					results <- id
				}
			}()
		}
		wg.Wait()
		close(results)
		close(errs)

		for err := range errs {
			t.Fatalf("Next() returned error under concurrency: %v", err)
		}

		seen := make(map[uint64]bool, total)
		count := 0
		for id := range results {
			count++
			if seen[id] {
				t.Fatalf("Next() returned duplicate fileID %d under concurrency", id)
			}
			seen[id] = true
		}

		if count != total {
			t.Fatalf("collected %d results; want exactly %d (%d goroutines x %d calls)", count, total, goroutines, perGoroutine)
		}
		if len(seen) != total {
			t.Fatalf("collected %d unique fileIDs; want exactly %d unique (no duplicates)", len(seen), total)
		}
	})

	t.Run("high-water-mark survives reopen, no collision", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "catalog.dat")

		fm1, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}

		alloc1, err := NewIDAllocator(fm1)
		if err != nil {
			t.Fatalf("NewIDAllocator(fm1) = _, %v; want nil error", err)
		}

		const preReopenAllocs = 25
		var maxID uint64
		for i := 0; i < preReopenAllocs; i++ {
			id, err := alloc1.Next()
			if err != nil {
				t.Fatalf("Next() #%d = _, %v; want nil error", i, err)
			}
			if id > maxID {
				maxID = id
			}
		}

		if err := alloc1.Close(); err != nil {
			t.Fatalf("alloc1.Close() = %v; want nil error", err)
		}
		if err := fm1.Close(); err != nil {
			t.Fatalf("fm1.Close() = %v; want nil error", err)
		}

		// Reopen: a brand-new FileManager + IDAllocator on the same underlying path.
		fm2, err := Open(path)
		if err != nil {
			t.Fatalf("re-Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm2.Close()

		alloc2, err := NewIDAllocator(fm2)
		if err != nil {
			t.Fatalf("NewIDAllocator(fm2) = _, %v; want nil error", err)
		}
		defer alloc2.Close()

		next, err := alloc2.Next()
		if err != nil {
			t.Fatalf("Next() after reopen = _, %v; want nil error", err)
		}
		if next <= maxID {
			t.Fatalf("Next() after reopen = %d; want strictly greater than pre-reopen max %d (no collision/reuse)", next, maxID)
		}
		if next != maxID+1 {
			t.Fatalf("Next() after reopen = %d; want exactly %d (maxID+1, no gap)", next, maxID+1)
		}
	})
}
