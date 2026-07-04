package catalog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
