package catalog

import (
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// newTestCatalog opens a fresh FileManager backed by an isolated t.TempDir() path
// (never DefaultCatalogFileName; see file.go's doc comment on why tests must not
// share that path) and wraps it in a Catalog, registering cleanup.
func newTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.dat")
	fm, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := fm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return NewCatalog(fm)
}

func testRecord(fileID uint64) CatalogRecord {
	return CatalogRecord{
		FileID:         fileID,
		PathHash:       fileID * 31,
		CurrentVersion: 1,
		SizeBytes:      1024,
		Status:         StatusActive,
		ParentTopicID:  0,
		LastModified:   1234567890,
	}
}

// --- Scenario 1: single-record Put+Get+Delete round-trip ---

func TestCatalogPutGetDeleteRoundTrip(t *testing.T) {
	c := newTestCatalog(t)

	rec := testRecord(1)
	if err := c.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := c.Get(1)
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Fatalf("Get after Put = %+v, want %+v", got, rec)
	}

	// Put again (overwrite) with a changed field, confirm Put overwrites in place
	// (no versioning of history — the new value fully replaces the old one).
	rec2 := rec
	rec2.SizeBytes = 2048
	rec2.CurrentVersion = 2
	if err := c.Put(rec2); err != nil {
		t.Fatalf("Put (overwrite): %v", err)
	}
	got2, err := c.Get(1)
	if err != nil {
		t.Fatalf("Get after overwrite Put: %v", err)
	}
	if !reflect.DeepEqual(got2, rec2) {
		t.Fatalf("Get after overwrite Put = %+v, want %+v", got2, rec2)
	}

	if err := c.Delete(1); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Get after Delete must return not-found, not a stale/old value.
	if _, err := c.Get(1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
}

// --- Scenario 2: concurrent Put/Get/Delete across many distinct fileIDs ---

func TestCatalogConcurrentDistinctFileIDs(t *testing.T) {
	c := newTestCatalog(t)

	const numFileIDs = 300 // > numStripes (256) so every stripe is exercised
	const numWorkersPerID = 4

	var wg sync.WaitGroup
	for fid := uint64(1); fid <= numFileIDs; fid++ {
		fid := fid
		rec := testRecord(fid)

		for w := 0; w < numWorkersPerID; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := c.Put(rec); err != nil {
					t.Errorf("Put(fileID=%d): %v", fid, err)
					return
				}
				// A concurrent Get may race with other workers' Puts for the SAME
				// fileID (all workers here Put the identical rec value, so any
				// successful Get must equal rec exactly — no torn/partial reads).
				if got, err := c.Get(fid); err != nil {
					t.Errorf("Get(fileID=%d): %v", fid, err)
				} else if !reflect.DeepEqual(got, rec) {
					t.Errorf("Get(fileID=%d) = %+v, want %+v (torn read/corruption)", fid, got, rec)
				}
			}()
		}
	}
	wg.Wait()

	// After all concurrent Puts/Gets complete, every fileID's final Get must match
	// exactly what was Put (no corruption, no lost updates, no data races - this test
	// is run with -race per the test spec).
	for fid := uint64(1); fid <= numFileIDs; fid++ {
		want := testRecord(fid)
		got, err := c.Get(fid)
		if err != nil {
			t.Fatalf("final Get(fileID=%d): %v", fid, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("final Get(fileID=%d) = %+v, want %+v", fid, got, want)
		}
	}

	// Now concurrently Delete every fileID and confirm each becomes not-found.
	var wg2 sync.WaitGroup
	for fid := uint64(1); fid <= numFileIDs; fid++ {
		fid := fid
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			if err := c.Delete(fid); err != nil {
				t.Errorf("Delete(fileID=%d): %v", fid, err)
			}
		}()
	}
	wg2.Wait()

	for fid := uint64(1); fid <= numFileIDs; fid++ {
		if _, err := c.Get(fid); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(fileID=%d) after concurrent Delete: err = %v, want ErrNotFound", fid, err)
		}
	}
}

// --- Scenario 3: stripe contention — different fileIDs must not serialize behind a
// held stripe lock ---
//
// Proof technique: rather than a fuzzy wall-clock/benchmark threshold (flaky under
// -race/CI load), this test directly acquires (in-package, since Go tests share the
// package) the record-stripe mutex for one fileID and holds it open-ended (until a
// channel is closed), then asserts via select-with-timeout that:
//   - an operation on a fileID in a DIFFERENT stripe completes quickly (does not wait
//     for the held stripe to be released) — proving stripes don't serialize unrelated
//     fileIDs;
//   - an operation on a fileID in the SAME stripe as the held lock does NOT complete
//     until the lock is released — proving the stripes are real locks (not no-ops),
//     which is the necessary control/cross-check for the first assertion to be
//     meaningful.
func TestCatalogStripesDoNotSerializeAcrossDifferentFileIDs(t *testing.T) {
	c := newTestCatalog(t)

	heldFileID := uint64(5)
	heldStripe := stripeFor(heldFileID)

	// Find a fileID that hashes to a DIFFERENT stripe than heldFileID.
	otherFileID := heldFileID
	for {
		otherFileID++
		if stripeFor(otherFileID) != heldStripe {
			break
		}
	}
	// Find a second fileID that hashes to the SAME stripe as heldFileID (but is a
	// distinct fileID), for the "must block" control assertion.
	sameStripeFileID := heldFileID
	for {
		sameStripeFileID += numStripes
		if sameStripeFileID != heldFileID {
			break
		}
	}
	if stripeFor(sameStripeFileID) != heldStripe {
		t.Fatalf("test setup bug: sameStripeFileID %d does not share heldFileID %d's stripe", sameStripeFileID, heldFileID)
	}

	// Seed a record for the same-stripe fileID so Get on it is meaningful (not just
	// an immediate not-found).
	if err := c.Put(testRecord(sameStripeFileID)); err != nil {
		t.Fatalf("Put(sameStripeFileID): %v", err)
	}

	// Acquire heldFileID's stripe lock directly (test-only access to the unexported
	// field) and hold it until release is closed.
	release := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		c.stripes[heldStripe].Lock()
		close(acquired)
		<-release
		c.stripes[heldStripe].Unlock()
	}()
	<-acquired

	// Assertion A: an operation on a DIFFERENT stripe's fileID must complete quickly,
	// without waiting for release.
	otherDone := make(chan error, 1)
	go func() {
		otherDone <- c.Put(testRecord(otherFileID))
	}()
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatalf("Put(otherFileID) on different stripe: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Put on a DIFFERENT stripe blocked behind an unrelated held stripe lock — striping is not working")
	}

	// Assertion B (control): an operation on the SAME stripe as the held lock must
	// NOT complete while the lock is held.
	sameStripeDone := make(chan error, 1)
	go func() {
		_, err := c.Get(sameStripeFileID)
		sameStripeDone <- err
	}()
	select {
	case <-sameStripeDone:
		t.Fatal("Get on the SAME stripe as a held lock completed before release — the stripe lock is not actually being honored by Get")
	case <-time.After(150 * time.Millisecond):
		// Expected: still blocked.
	}

	close(release)

	select {
	case err := <-sameStripeDone:
		if err != nil {
			t.Fatalf("Get(sameStripeFileID) after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get on the same stripe never completed after release")
	}
}

// --- Scenario 4: Get/Delete on a nonexistent fileID returns a clear not-found error ---

func TestCatalogGetDeleteNotFound(t *testing.T) {
	c := newTestCatalog(t)

	const missing = uint64(999)

	_, err := c.Get(missing)
	if err == nil {
		t.Fatal("Get(missing) returned nil error, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want wrapping ErrNotFound", err)
	}

	err = c.Delete(missing)
	if err == nil {
		t.Fatal("Delete(missing) returned nil error, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(missing) err = %v, want wrapping ErrNotFound", err)
	}
}

// Sanity check that stripeFor is deterministic and spans the documented stripe count,
// used as a building block by the contention test above.
func TestCatalogStripeForSanity(t *testing.T) {
	seen := make(map[uint64]bool)
	for fid := uint64(1); fid <= numStripes*2; fid++ {
		seen[stripeFor(fid)] = true
	}
	if len(seen) != numStripes {
		t.Fatalf("stripeFor over %d fileIDs only touched %d distinct stripes, want %d", numStripes*2, len(seen), numStripes)
	}
	for s := uint64(0); s < numStripes; s++ {
		if !seen[s] {
			t.Fatalf("stripe %d never hit", s)
		}
	}
}
