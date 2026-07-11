package catalog

import (
	"encoding/binary"
	"os"
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

// writeSidecarHighWaterMark overwrites the .idalloc sidecar file alongside
// catalogPath with the given high-water-mark value, simulating an
// independently-restored (stale) sidecar.
func writeSidecarHighWaterMark(t *testing.T, catalogPath string, hwm uint64) {
	t.Helper()

	var buf [idAllocStateSize]byte
	binary.LittleEndian.PutUint64(buf[:], hwm)

	if err := os.WriteFile(catalogPath+idAllocSuffix, buf[:], 0o644); err != nil {
		t.Fatalf("writing sidecar %s: %v", catalogPath+idAllocSuffix, err)
	}
}

// TestIDAllocatorCrossChecksCatalogHighWaterMark exercises the test spec for
// subtask 4.5.5.2: construct a catalog.dat with FileIDs beyond a
// fresh/mismatched sidecar's high-water-mark, and assert NewIDAllocator detects
// the mismatch and returns an explicit error rather than silently risking
// fileID collisions.
func TestIDAllocatorCrossChecksCatalogHighWaterMark(t *testing.T) {
	// buildCatalogWithRecords creates a fresh catalog.dat at t.TempDir()/catalog.dat,
	// allocates n fileIDs via a normal IDAllocator, Puts a CatalogRecord for each one
	// (so they are actually durably present in catalog.dat, not just handed out by
	// the allocator), then closes everything down cleanly. It returns the catalog
	// path and the highest fileID that was Put.
	buildCatalogWithRecords := func(t *testing.T, n int) (path string, maxFileID uint64) {
		t.Helper()

		dir := t.TempDir()
		path = filepath.Join(dir, "catalog.dat")

		fm, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}

		cat := NewCatalog(fm)
		alloc, err := NewIDAllocator(fm)
		if err != nil {
			t.Fatalf("NewIDAllocator(fm) = _, %v; want nil error", err)
		}

		for i := 0; i < n; i++ {
			id, err := alloc.Next()
			if err != nil {
				t.Fatalf("Next() #%d = _, %v; want nil error", i, err)
			}
			if err := cat.Put(CatalogRecord{FileID: id, SizeBytes: uint64(i)}); err != nil {
				t.Fatalf("Put(fileID=%d) = %v; want nil error", id, err)
			}
			if id > maxFileID {
				maxFileID = id
			}
		}

		if err := alloc.Close(); err != nil {
			t.Fatalf("alloc.Close() = %v; want nil error", err)
		}
		if err := fm.Close(); err != nil {
			t.Fatalf("fm.Close() = %v; want nil error", err)
		}

		return path, maxFileID
	}

	t.Run("sidecar lost (deleted) against non-fresh catalog.dat is detected", func(t *testing.T) {
		path, maxFileID := buildCatalogWithRecords(t, 5)
		if maxFileID == 0 {
			t.Fatalf("test setup: maxFileID = 0; want > 0")
		}

		// Simulate the sidecar being lost: remove it entirely. A subsequent
		// NewIDAllocator will treat this exactly like a fresh catalog (high-water-mark
		// 0), which must be detected as a mismatch against the non-fresh catalog.dat.
		if err := os.Remove(path + idAllocSuffix); err != nil {
			t.Fatalf("os.Remove(sidecar) = %v; want nil error", err)
		}

		fm, err := Open(path)
		if err != nil {
			t.Fatalf("re-Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm.Close()

		if _, err := NewIDAllocator(fm); err == nil {
			t.Fatalf("NewIDAllocator(fm) with lost sidecar = nil error; want non-nil error (mismatch against catalog.dat's max fileID %d)", maxFileID)
		}
	})

	t.Run("sidecar independently restored to a stale value is detected", func(t *testing.T) {
		path, maxFileID := buildCatalogWithRecords(t, 5)
		if maxFileID < 2 {
			t.Fatalf("test setup: maxFileID = %d; want >= 2 so a stale value below it is meaningful", maxFileID)
		}

		// Simulate an independently-restored (stale) sidecar: overwrite it with a
		// high-water-mark strictly less than the max fileID actually in catalog.dat.
		writeSidecarHighWaterMark(t, path, maxFileID-1)

		fm, err := Open(path)
		if err != nil {
			t.Fatalf("re-Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm.Close()

		if _, err := NewIDAllocator(fm); err == nil {
			t.Fatalf("NewIDAllocator(fm) with stale sidecar (hwm=%d) = nil error; want non-nil error (catalog.dat's max fileID is %d)", maxFileID-1, maxFileID)
		}
	})

	t.Run("consistent sidecar (no mismatch) still opens successfully", func(t *testing.T) {
		path, maxFileID := buildCatalogWithRecords(t, 5)

		fm, err := Open(path)
		if err != nil {
			t.Fatalf("re-Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm.Close()

		alloc, err := NewIDAllocator(fm)
		if err != nil {
			t.Fatalf("NewIDAllocator(fm) with consistent sidecar = _, %v; want nil error", err)
		}
		defer alloc.Close()

		next, err := alloc.Next()
		if err != nil {
			t.Fatalf("Next() = _, %v; want nil error", err)
		}
		if next != maxFileID+1 {
			t.Fatalf("Next() = %d; want %d (maxFileID+1, no false-positive mismatch)", next, maxFileID+1)
		}
	})

	t.Run("sidecar high-water-mark ahead of catalog.dat's max fileID is NOT a mismatch", func(t *testing.T) {
		// Allocate more fileIDs than are actually Put into the catalog (simulating
		// fileIDs that were allocated but never written, or written and later
		// deleted); the sidecar's high-water-mark being ahead of catalog.dat's max is
		// expected and must not be flagged as an error.
		dir := t.TempDir()
		path := filepath.Join(dir, "catalog.dat")

		fm, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q) = _, %v; want nil error", path, err)
		}

		cat := NewCatalog(fm)
		alloc, err := NewIDAllocator(fm)
		if err != nil {
			t.Fatalf("NewIDAllocator(fm) = _, %v; want nil error", err)
		}

		var lastPutID uint64
		for i := 0; i < 5; i++ {
			id, err := alloc.Next()
			if err != nil {
				t.Fatalf("Next() #%d = _, %v; want nil error", i, err)
			}
			// Only Put the first 3 of the 5 allocated IDs, so the sidecar's
			// high-water-mark (5) ends up ahead of catalog.dat's max fileID (3).
			if i < 3 {
				if err := cat.Put(CatalogRecord{FileID: id}); err != nil {
					t.Fatalf("Put(fileID=%d) = %v; want nil error", id, err)
				}
				lastPutID = id
			}
		}
		if lastPutID != 3 {
			t.Fatalf("test setup: lastPutID = %d; want 3", lastPutID)
		}

		if err := alloc.Close(); err != nil {
			t.Fatalf("alloc.Close() = %v; want nil error", err)
		}
		if err := fm.Close(); err != nil {
			t.Fatalf("fm.Close() = %v; want nil error", err)
		}

		fm2, err := Open(path)
		if err != nil {
			t.Fatalf("re-Open(%q) = _, %v; want nil error", path, err)
		}
		defer fm2.Close()

		alloc2, err := NewIDAllocator(fm2)
		if err != nil {
			t.Fatalf("NewIDAllocator(fm2) = _, %v; want nil error (sidecar ahead of catalog.dat max is not a mismatch)", err)
		}
		defer alloc2.Close()

		next, err := alloc2.Next()
		if err != nil {
			t.Fatalf("Next() = _, %v; want nil error", err)
		}
		if next != 6 {
			t.Fatalf("Next() = %d; want 6 (sidecar's high-water-mark of 5, +1)", next)
		}
	})
}
