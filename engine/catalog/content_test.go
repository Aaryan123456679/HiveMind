package catalog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// newTestContentStore wires up an isolated FileManager+Catalog, wal.Writer, and
// ContentStore under a fresh t.TempDir(), registering cleanup for the FileManager and
// wal.Writer. Returns the ContentStore, the underlying Catalog (for direct visibility
// assertions), and the wal directory (for direct WAL segment assertions).
func newTestContentStore(t *testing.T) (cs *ContentStore, cat *Catalog, walDir string) {
	t.Helper()

	root := t.TempDir()

	fm, err := Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		t.Fatalf("Open (catalog FileManager): %v", err)
	}
	t.Cleanup(func() {
		if err := fm.Close(); err != nil {
			t.Errorf("FileManager.Close: %v", err)
		}
	})
	cat = NewCatalog(fm)

	walDir = filepath.Join(root, "wal")
	w, err := wal.OpenWriter(walDir, 1<<20)
	if err != nil {
		t.Fatalf("wal.OpenWriter: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("wal.Writer.Close: %v", err)
		}
	})

	cs, err = OpenContentStore(root, cat, w)
	if err != nil {
		t.Fatalf("OpenContentStore: %v", err)
	}

	return cs, cat, walDir
}

func testContentRecord(fileID uint64) CatalogRecord {
	return CatalogRecord{
		FileID:         fileID,
		PathHash:       fileID * 31,
		CurrentVersion: 1,
		SizeBytes:      0, // set by caller once content bytes are known, if desired
		Status:         StatusActive,
		ParentTopicID:  0,
		LastModified:   1234567890,
	}
}

// TestContentCreate covers this subtask's full test spec in one test: creating a new
// topic file writes content/<fileID>.v1.md, the corresponding catalog mutation is logged
// to the WAL before the file is considered committed (WAL-before-apply, proven the same
// way engine/wal/record_test.go's TestFsyncBeforeApply proves wal.AppendAndApply's own
// ordering guarantee: observe durable-on-disk state from inside the apply callback), and
// the content bytes on disk match the input.
func TestContentCreate(t *testing.T) {
	cs, cat, walDir := newTestContentStore(t)

	const fileID = uint64(42)
	data := []byte("# Hello Topic\n\nSome markdown body.\n")
	rec := testContentRecord(fileID)
	rec.SizeBytes = uint64(len(data))

	var (
		hookRan               bool
		sawWALDurableAtHook   bool
		sawCatalogNotVisible  bool
		walRecordFileIDAtHook uint64
	)

	afterWALBeforeApply := func() {
		hookRan = true

		// Independently re-read the WAL segment from disk (fresh os.ReadFile via
		// wal.ReadSegment, no shared state with the Writer) to confirm the
		// catalog-Put record is already durable at this point, mirroring
		// TestFsyncBeforeApply's observation technique.
		segPath := filepath.Join(walDir, "wal-0.log")
		records, err := wal.ReadSegment(segPath)
		if err != nil {
			t.Fatalf("ReadSegment inside hook: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("ReadSegment inside hook: got %d records, want 1 (WAL record must already be durable before apply)", len(records))
		}
		decoded, err := wal.DecodeTypedRecord(records[0])
		if err != nil {
			t.Fatalf("DecodeTypedRecord inside hook: %v", err)
		}
		put, err := decoded.AsCatalogPut()
		if err != nil {
			t.Fatalf("AsCatalogPut inside hook: %v", err)
		}
		walRecordFileIDAtHook = put.FileID
		sawWALDurableAtHook = true

		// The content file and catalog record must NOT be visible/committed yet:
		// apply (content write + catalog Put) has not run at this point.
		if _, err := os.Stat(cs.ContentPath(fileID)); err == nil {
			t.Fatalf("content file already exists inside hook, before apply ran")
		} else if !os.IsNotExist(err) {
			t.Fatalf("unexpected error stat-ing content path inside hook: %v", err)
		}
		if _, err := cat.Get(fileID); errors.Is(err, ErrNotFound) {
			sawCatalogNotVisible = true
		} else if err != nil {
			t.Fatalf("unexpected error from cat.Get inside hook: %v", err)
		} else {
			t.Fatalf("cat.Get succeeded inside hook, before apply ran: catalog record must not be visible yet")
		}
	}

	offset, err := cs.createWithHook(rec, data, afterWALBeforeApply)
	if err != nil {
		t.Fatalf("createWithHook: %v", err)
	}
	if offset != 0 {
		t.Errorf("offset = %d, want 0 (first record in a fresh WAL segment)", offset)
	}

	if !hookRan {
		t.Fatal("afterWALBeforeApply hook did not run")
	}
	if !sawWALDurableAtHook {
		t.Fatal("WAL record was not durable at hook time; WAL-before-apply ordering did not hold")
	}
	if walRecordFileIDAtHook != fileID {
		t.Fatalf("WAL record FileID observed at hook time = %d, want %d", walRecordFileIDAtHook, fileID)
	}
	if !sawCatalogNotVisible {
		t.Fatal("catalog record was already visible at hook time; WAL-before-catalog-visibility ordering did not hold")
	}

	// After Create returns: content bytes on disk must match input exactly.
	gotData, err := os.ReadFile(cs.ContentPath(fileID))
	if err != nil {
		t.Fatalf("reading content file after Create: %v", err)
	}
	if string(gotData) != string(data) {
		t.Fatalf("content file bytes = %q, want %q", gotData, data)
	}

	// Content path must literally be content/<fileID>.v1.md.
	wantSuffix := filepath.Join("content", "42.v1.md")
	if got := cs.ContentPath(fileID); filepath.Base(filepath.Dir(got))+string(filepath.Separator)+filepath.Base(got) != wantSuffix {
		t.Fatalf("ContentPath(%d) = %q, want path ending in %q", fileID, got, wantSuffix)
	}

	// And the catalog record must now be visible/committed.
	gotRec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("cat.Get after Create: %v", err)
	}
	if !reflect.DeepEqual(gotRec, rec) {
		t.Fatalf("cat.Get after Create = %+v, want %+v", gotRec, rec)
	}
}

// TestContentCreateInvalidFileID confirms Create rejects the reserved InvalidFileID
// sentinel rather than silently writing a bogus content file / WAL record.
func TestContentCreateInvalidFileID(t *testing.T) {
	cs, _, _ := newTestContentStore(t)

	rec := testContentRecord(InvalidFileID)
	if _, err := cs.Create(rec, []byte("data")); err == nil {
		t.Fatal("Create with InvalidFileID: want error, got nil")
	}
}

// TestContentRead covers subtask 1.4.2's full test spec: writing content via
// Create then reading it back via Read must return byte-for-byte identical
// content to what was written.
func TestContentRead(t *testing.T) {
	cs, _, _ := newTestContentStore(t)

	const fileID = uint64(7)
	data := []byte("# Read Path\n\nContent written then read back verbatim.\n")
	rec := testContentRecord(fileID)
	rec.SizeBytes = uint64(len(data))

	if _, err := cs.Create(rec, data); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := cs.Read(fileID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Read(%d) = %q, want %q", fileID, got, data)
	}
}

// TestContentAppend covers subtask 1.4.3's full test spec: repeatedly appending
// small chunks to an existing file must (a) keep the on-disk content and the
// catalog's SizeBytes in lockstep with the cumulative appended length, and (b)
// report the threshold-crossing signal true on exactly one append (the one
// that pushes the cumulative size from at-or-under the threshold to strictly
// over it), and false on every other append (both before and after crossing).
//
// A small overridden threshold (rather than the real ~8KB default) is used so
// the test can exercise the exact-once crossing semantics with a short,
// fast-running loop instead of writing kilobytes of filler content.
func TestContentAppend(t *testing.T) {
	cs, cat, _ := newTestContentStore(t)
	cs.splitThresholdBytes = 64

	const fileID = uint64(99)
	initial := []byte("start")
	rec := testContentRecord(fileID)
	rec.SizeBytes = uint64(len(initial))
	if _, err := cs.Create(rec, initial); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var (
		cumulative    = append([]byte(nil), initial...)
		crossingCount int
		crossingIdx   = -1
	)

	chunk := []byte("0123456789") // 10 bytes per append

	for i := 0; i < 10; i++ {
		crossed, err := cs.Append(fileID, chunk)
		if err != nil {
			t.Fatalf("Append(#%d): %v", i, err)
		}
		cumulative = append(cumulative, chunk...)

		if crossed {
			crossingCount++
			crossingIdx = i
		}

		// SizeBytes must track cumulative content length after every append.
		gotRec, err := cat.Get(fileID)
		if err != nil {
			t.Fatalf("cat.Get after Append(#%d): %v", i, err)
		}
		if gotRec.SizeBytes != uint64(len(cumulative)) {
			t.Fatalf("Append(#%d): SizeBytes = %d, want %d", i, gotRec.SizeBytes, len(cumulative))
		}

		// Content on disk must match the cumulative bytes exactly.
		got, err := cs.Read(fileID)
		if err != nil {
			t.Fatalf("Read after Append(#%d): %v", i, err)
		}
		if !bytes.Equal(got, cumulative) {
			t.Fatalf("Read after Append(#%d) = %q, want %q", i, got, cumulative)
		}

		// Signal correctness relative to the threshold at this point.
		wantCrossed := uint64(len(cumulative)-len(chunk)) <= cs.splitThresholdBytes && uint64(len(cumulative)) > cs.splitThresholdBytes
		if crossed != wantCrossed {
			t.Fatalf("Append(#%d): crossed = %v, want %v (cumulative size %d, threshold %d)", i, crossed, wantCrossed, len(cumulative), cs.splitThresholdBytes)
		}
	}

	if crossingCount != 1 {
		t.Fatalf("threshold-crossing signal fired %d times, want exactly 1 (at append index %d)", crossingCount, crossingIdx)
	}
}

// TestContentAppendNotFound confirms Append reports a wrapped ErrNotFound for
// a fileID that was never created, mirroring Read's behavior.
func TestContentAppendNotFound(t *testing.T) {
	cs, _, _ := newTestContentStore(t)

	const missingFileID = uint64(1000)
	crossed, err := cs.Append(missingFileID, []byte("data"))
	if crossed {
		t.Fatalf("Append(missing) crossed = true, want false")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Append(missing) err = %v, want wrapping ErrNotFound", err)
	}
}

// TestContentAppendConcurrentSameFileID is a regression test for the fix cycle
// responding to 1.4.3's verification finding: Append's read-modify-write of the
// content file was unsynchronized, so concurrent Append calls against the SAME
// fileID could race, each read the same pre-append bytes, and each write back a
// result reflecting only its own appended data -- silently losing every other
// goroutine's update (reproduced upstream as 49/50 one-byte appends lost, final
// content length 1 instead of 50, with catalog SizeBytes matching the corrupted
// result and no error surfaced anywhere). This test reproduces that exact repro
// shape (N concurrent 1-byte Append calls to one fileID) and asserts the final
// content length and catalog SizeBytes reflect ALL appends, not a lost-update
// count. Must be run with -race (per this repo's test spec) to also catch any
// data race the fix might reintroduce, not just the logical lost-update outcome.
func TestContentAppendConcurrentSameFileID(t *testing.T) {
	cs, cat, _ := newTestContentStore(t)

	const fileID = uint64(7)
	rec := testContentRecord(fileID)
	if _, err := cs.Create(rec, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	const numAppends = 50 // matches the verification agent's exact repro count

	var wg sync.WaitGroup
	for i := 0; i < numAppends; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cs.Append(fileID, []byte("x")); err != nil {
				t.Errorf("Append: %v", err)
			}
		}()
	}
	wg.Wait()

	got, err := cs.Read(fileID)
	if err != nil {
		t.Fatalf("Read after concurrent Appends: %v", err)
	}
	if len(got) != numAppends {
		t.Fatalf("content length after %d concurrent 1-byte Appends = %d, want %d (lost update)", numAppends, len(got), numAppends)
	}

	gotRec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("cat.Get after concurrent Appends: %v", err)
	}
	if gotRec.SizeBytes != uint64(numAppends) {
		t.Fatalf("SizeBytes after %d concurrent 1-byte Appends = %d, want %d (lost update)", numAppends, gotRec.SizeBytes, numAppends)
	}
}

// TestContentReadNotFound confirms Read reports a wrapped ErrNotFound (rather
// than an os.ReadFile-shaped error) for a fileID that was never created, so
// callers can distinguish "never created" from other read failures the same
// way catalog.go's Get/Delete already let callers distinguish ErrNotFound.
func TestContentReadNotFound(t *testing.T) {
	cs, _, _ := newTestContentStore(t)

	const missingFileID = uint64(999)
	got, err := cs.Read(missingFileID)
	if got != nil {
		t.Fatalf("Read(missing) data = %q, want nil", got)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read(missing) err = %v, want wrapping ErrNotFound", err)
	}
}
