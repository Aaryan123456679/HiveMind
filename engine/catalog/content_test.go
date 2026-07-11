package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// TestContentCreateDuplicateFileID covers subtask 4.5.5.4's core acceptance
// criterion: calling Create a second time for the same fileID is legal and performs a
// full, last-write-wins overwrite of both the content file and the catalog record (see
// content.go's Create doc comment for the documented semantics this pins down). The
// second Create's data and rec must entirely supersede the first's -- nothing from the
// first call may survive (stale bytes, stale catalog fields) or leak (orphaned files in
// the content directory).
func TestContentCreateDuplicateFileID(t *testing.T) {
	cs, cat, _ := newTestContentStore(t)

	const fileID = uint64(123)

	dataA := []byte("# First Version\n\nOriginal body.\n")
	recA := testContentRecord(fileID)
	recA.SizeBytes = uint64(len(dataA))
	recA.LastModified = 1000

	if _, err := cs.Create(recA, dataA); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	dataB := []byte("# Second Version\n\nCompletely different, longer replacement body.\n")
	recB := testContentRecord(fileID)
	recB.SizeBytes = uint64(len(dataB))
	recB.LastModified = 2000

	if _, err := cs.Create(recB, dataB); err != nil {
		t.Fatalf("second (duplicate-fileID) Create: %v", err)
	}

	// Read must return the SECOND call's bytes exactly -- not the first's, and not a
	// concatenation of both.
	got, err := cs.Read(fileID)
	if err != nil {
		t.Fatalf("Read after duplicate Create: %v", err)
	}
	if !bytes.Equal(got, dataB) {
		t.Fatalf("Read after duplicate Create = %q, want %q (second Create's data)", got, dataB)
	}

	// The catalog record must reflect the SECOND call's rec exactly.
	gotRec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("cat.Get after duplicate Create: %v", err)
	}
	if !reflect.DeepEqual(gotRec, recB) {
		t.Fatalf("cat.Get after duplicate Create = %+v, want %+v (second Create's rec)", gotRec, recB)
	}

	// No leaked/orphaned files: exactly one file must exist in the content directory
	// for this fileID (the final content/<fileID>.v1.md), and no stray temp files left
	// behind by writeContentFile's write-temp-then-rename technique.
	entries, err := os.ReadDir(cs.dir)
	if err != nil {
		t.Fatalf("os.ReadDir(cs.dir): %v", err)
	}
	var matching []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), fmt.Sprintf("%d", fileID)) {
			matching = append(matching, e.Name())
		}
	}
	wantName := fmt.Sprintf("%d%s", fileID, contentVersionSuffix)
	if len(matching) != 1 || matching[0] != wantName {
		t.Fatalf("content dir entries for fileID %d = %v, want exactly [%q] (no leaked/orphaned files after duplicate Create)", fileID, matching, wantName)
	}
}

// TestContentCreateEmptyAndLargeContent covers subtask 4.5.5.4's empty-content and
// very-large-content input coverage requirement: Create/Read must round-trip
// zero-length content faithfully (as a zero-length result, not an error), and must
// round-trip a multi-megabyte payload byte-for-byte, with the catalog's SizeBytes
// matching in both cases.
func TestContentCreateEmptyAndLargeContent(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		cs, cat, _ := newTestContentStore(t)

		const fileID = uint64(1)
		data := []byte{}
		rec := testContentRecord(fileID)
		rec.SizeBytes = 0

		if _, err := cs.Create(rec, data); err != nil {
			t.Fatalf("Create with empty content: %v", err)
		}

		got, err := cs.Read(fileID)
		if err != nil {
			t.Fatalf("Read after empty Create: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("Read after empty Create = %q (len %d), want zero-length", got, len(got))
		}

		gotRec, err := cat.Get(fileID)
		if err != nil {
			t.Fatalf("cat.Get after empty Create: %v", err)
		}
		if gotRec.SizeBytes != 0 {
			t.Fatalf("SizeBytes after empty Create = %d, want 0", gotRec.SizeBytes)
		}
	})

	t.Run("large", func(t *testing.T) {
		cs, cat, _ := newTestContentStore(t)

		const fileID = uint64(2)
		const largeSize = 8 * 1024 * 1024 // 8 MiB, well over defaultSplitThresholdBytes

		data := make([]byte, largeSize)
		for i := range data {
			data[i] = byte('A' + (i % 26)) // deterministic, non-uniform fill pattern
		}

		rec := testContentRecord(fileID)
		rec.SizeBytes = uint64(len(data))

		if _, err := cs.Create(rec, data); err != nil {
			t.Fatalf("Create with large content: %v", err)
		}

		got, err := cs.Read(fileID)
		if err != nil {
			t.Fatalf("Read after large Create: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("Read after large Create returned %d bytes, want %d bytes matching the original content byte-for-byte", len(got), len(data))
		}

		gotRec, err := cat.Get(fileID)
		if err != nil {
			t.Fatalf("cat.Get after large Create: %v", err)
		}
		if gotRec.SizeBytes != uint64(largeSize) {
			t.Fatalf("SizeBytes after large Create = %d, want %d", gotRec.SizeBytes, largeSize)
		}
	})
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

// openGenContentStore opens (or reopens) a FileManager+Catalog+wal.Writer+ContentStore
// generation rooted at root, registering t.Cleanup to close the FileManager and
// wal.Writer. If recover is true, it calls RecoverFromWAL(cat, walDir) to reconstruct
// cat's in-memory index from the WAL before wiring the ContentStore, simulating what a
// restarted process must do given Catalog's documented "empty index on load" gap (see
// catalog.go's Catalog doc comment and recovery.go's RecoverFromWAL doc comment).
//
// This is the "reopen catalog + content store from disk" seam TestContentDurabilityRestart
// exercises: unlike newTestContentStore (which only ever opens one generation of handles for
// the lifetime of a test), openGenContentStore can be called twice against the SAME root to
// model a process exit (first generation's handles closed) followed by a fresh process
// startup (second generation's handles opened against the same on-disk files).
func openGenContentStore(t *testing.T, root, walDir string, recoverIndex bool) (cs *ContentStore, cat *Catalog) {
	t.Helper()

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

	if recoverIndex {
		if err := RecoverFromWAL(cat, walDir); err != nil {
			t.Fatalf("RecoverFromWAL: %v", err)
		}
	}

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

	return cs, cat
}

// TestContentDurabilityRestart covers subtask 1.4.4's full test spec: after writing
// (Create) and appending (Append) content, simulate a process restart by closing the
// original FileManager+wal.Writer (without deleting any on-disk WAL/content/catalog
// files) and opening a brand-new FileManager+Catalog+wal.Writer+ContentStore generation
// against the same root, reconstructing the new Catalog's index via RecoverFromWAL.
// Reading the fileID's content via the NEW ContentStore must return the exact same bytes
// that were visible via the OLD ContentStore immediately before the simulated restart.
func TestContentDurabilityRestart(t *testing.T) {
	root := t.TempDir()
	walDir := filepath.Join(root, "wal")

	const fileID = uint64(7)
	initial := []byte("# Restart Topic\n\nInitial body.\n")
	appendA := []byte("More content, appended once.\n")
	appendB := []byte("And a second append, for good measure.\n")
	want := append(append(append([]byte{}, initial...), appendA...), appendB...)

	// Generation 1: fresh FileManager+Catalog+wal.Writer+ContentStore, write + append.
	fm1, err := Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		t.Fatalf("Open (gen1 FileManager): %v", err)
	}
	cat1 := NewCatalog(fm1)

	w1, err := wal.OpenWriter(walDir, 1<<20)
	if err != nil {
		t.Fatalf("wal.OpenWriter (gen1): %v", err)
	}

	cs1, err := OpenContentStore(root, cat1, w1)
	if err != nil {
		t.Fatalf("OpenContentStore (gen1): %v", err)
	}

	rec := testContentRecord(fileID)
	rec.SizeBytes = uint64(len(initial))
	if _, err := cs1.Create(rec, initial); err != nil {
		t.Fatalf("gen1 Create: %v", err)
	}
	if _, err := cs1.Append(fileID, appendA); err != nil {
		t.Fatalf("gen1 Append A: %v", err)
	}
	if _, err := cs1.Append(fileID, appendB); err != nil {
		t.Fatalf("gen1 Append B: %v", err)
	}

	gotBeforeRestart, err := cs1.Read(fileID)
	if err != nil {
		t.Fatalf("gen1 Read (before restart): %v", err)
	}
	if !bytes.Equal(gotBeforeRestart, want) {
		t.Fatalf("gen1 Read (before restart) = %q, want %q", gotBeforeRestart, want)
	}

	// Simulate a process restart: close generation 1's handles WITHOUT deleting any
	// on-disk WAL/content/catalog files, then open a brand-new generation against the
	// same root.
	if err := w1.Close(); err != nil {
		t.Fatalf("gen1 wal.Writer.Close: %v", err)
	}
	if err := fm1.Close(); err != nil {
		t.Fatalf("gen1 FileManager.Close: %v", err)
	}

	// Generation 2 ("after restart"): brand-new Catalog starts with an EMPTY in-memory
	// index (see catalog.go's documented gap); RecoverFromWAL must reconstruct it from
	// the WAL before this new ContentStore's Read can see fileID at all.
	cs2, _ := openGenContentStore(t, root, walDir, true /* recover */)

	gotAfterRestart, err := cs2.Read(fileID)
	if err != nil {
		t.Fatalf("gen2 Read (after restart): %v", err)
	}
	if !bytes.Equal(gotAfterRestart, want) {
		t.Fatalf("gen2 Read (after restart) = %q, want %q (byte-for-byte match with pre-restart content)", gotAfterRestart, want)
	}
}

// TestReadPartialComputesHeaderOffsets covers issue #13's subtask 2b.4.1: ReadPartial
// scans a fileID's content for ATX markdown headers and returns their byte offsets, in
// order, computing it lazily on first call.
func TestReadPartialComputesHeaderOffsets(t *testing.T) {
	cs, _, _ := newTestContentStore(t)

	const fileID = uint64(7)
	content := []byte("intro text\n# Title\nbody\n## Sub\nmore body\n")
	rec := testContentRecord(fileID)
	rec.SizeBytes = uint64(len(content))
	if _, err := cs.Create(rec, content); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := cs.ReadPartial(fileID)
	if err != nil {
		t.Fatalf("ReadPartial: %v", err)
	}

	want := []HeaderOffset{
		{Header: "# Title", Offset: 11},
		{Header: "## Sub", Offset: 24},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadPartial = %+v, want %+v", got, want)
	}
}

// TestReadPartialNotFound confirms ReadPartial reports a wrapped ErrNotFound for a
// fileID that was never created, mirroring Read's and Append's behavior.
func TestReadPartialNotFound(t *testing.T) {
	cs, _, _ := newTestContentStore(t)

	if _, err := cs.ReadPartial(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadPartial(unknown fileID): err = %v, want wrapped ErrNotFound", err)
	}
}

// TestAppendInvalidatesHeaderCache covers issue #13's subtask 2b.4.1's core acceptance
// criteria: Append (a transaction that changes file boundaries) invalidates fileID's
// header-offset cache atomically, so a ReadPartial call after Append never serves
// offsets computed against the pre-Append content.
func TestAppendInvalidatesHeaderCache(t *testing.T) {
	cs, _, _ := newTestContentStore(t)

	const fileID = uint64(42)
	initial := []byte("# First\nbody\n")
	rec := testContentRecord(fileID)
	rec.SizeBytes = uint64(len(initial))
	if _, err := cs.Create(rec, initial); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Populate the cache against the pre-Append content.
	before, err := cs.ReadPartial(fileID)
	if err != nil {
		t.Fatalf("ReadPartial (before Append): %v", err)
	}
	wantBefore := []HeaderOffset{{Header: "# First", Offset: 0}}
	if !reflect.DeepEqual(before, wantBefore) {
		t.Fatalf("ReadPartial (before Append) = %+v, want %+v", before, wantBefore)
	}

	appended := []byte("## Second\nmore\n")
	if _, err := cs.Append(fileID, appended); err != nil {
		t.Fatalf("Append: %v", err)
	}

	after, err := cs.ReadPartial(fileID)
	if err != nil {
		t.Fatalf("ReadPartial (after Append): %v", err)
	}
	wantAfter := []HeaderOffset{
		{Header: "# First", Offset: 0},
		{Header: "## Second", Offset: len(initial)},
	}
	if !reflect.DeepEqual(after, wantAfter) {
		t.Fatalf("ReadPartial (after Append) = %+v, want %+v (stale cache from before Append was not invalidated)", after, wantAfter)
	}
}

// contentChunkHeaderLen is the fixed-width prefix makeContentChunk/
// parseContentChunks agree on for each self-describing chunk: "<<" (2 bytes)
// + a 4-digit decimal body length (4 bytes) + ":" (1 byte) = 7 bytes, before
// the variable-length body itself.
const contentChunkHeaderLen = 7

// contentChunkFooter closes every chunk makeContentChunk produces. Its
// presence (unmangled) immediately after exactly the declared body length is
// what lets parseContentChunks distinguish a well-formed chunk boundary from
// a torn/truncated one.
const contentChunkFooter = ">>\n"

// makeContentChunk returns the i-th self-describing test chunk: a 4-digit
// decimal length prefix, a body of exactly that many 'Z' bytes, and a fixed
// footer -- e.g. "<<0023:ZZZZZZZZZZZZZZZZZZZZZZZ>>\n". Varying the body
// length by i keeps chunk sizes non-uniform (closer to real markdown
// appends of varying size) without needing any cross-goroutine bookkeeping:
// each chunk fully describes and validates itself (see parseContentChunks),
// which is what makes TestContentAppendConcurrentRead's torn-read check
// robust against not knowing the actual commit order concurrent Append
// calls land in.
func makeContentChunk(i int) []byte {
	n := 20 + (i % 40)
	body := strings.Repeat("Z", n)
	return []byte(fmt.Sprintf("<<%04d:%s%s", n, body, contentChunkFooter))
}

// parseContentChunks reports whether content is exactly initial followed by
// zero or more well-formed makeContentChunk-shaped chunks back-to-back, with
// nothing left over. It is the empirical torn-read detector for
// TestContentAppendConcurrentRead: content written via writeContentFile's
// write-temp-then-rename technique must always be observed by a concurrent
// Read as either fully-old or fully-new (see content.go's Read doc comment
// and writeContentFile's doc comment) -- never a partial rename mid-flight,
// never a mix of two different Append calls' bytes. Any of those failure
// modes would corrupt one or more chunk headers/bodies/footers or leave a
// truncated tail, which this parser rejects.
//
// This intentionally does NOT depend on knowing which chunks committed in
// what order (concurrent Append calls interleave in an order this test does
// not control and cannot know without racing its own bookkeeping against the
// system under test) -- each chunk is self-validating in isolation, so this
// can be called against ANY snapshot cs.Read returns at ANY point during the
// concurrent phase.
func parseContentChunks(content, initial []byte) error {
	if !bytes.HasPrefix(content, initial) {
		return fmt.Errorf("content does not start with the expected initial prefix %q (got %d bytes total)", initial, len(content))
	}
	rest := content[len(initial):]

	for len(rest) > 0 {
		if len(rest) < contentChunkHeaderLen {
			return fmt.Errorf("truncated chunk header: only %d bytes left, want at least %d", len(rest), contentChunkHeaderLen)
		}
		if rest[0] != '<' || rest[1] != '<' {
			return fmt.Errorf("malformed chunk header: want %q prefix, got %q", "<<", rest[:2])
		}
		if rest[6] != ':' {
			return fmt.Errorf("malformed chunk header: want ':' at offset 6, got %q", rest[6])
		}
		n, err := strconv.Atoi(string(rest[2:6]))
		if err != nil {
			return fmt.Errorf("malformed chunk length field %q: %w", rest[2:6], err)
		}
		if n < 0 {
			return fmt.Errorf("malformed chunk length field %q: negative", rest[2:6])
		}

		wantTotal := contentChunkHeaderLen + n + len(contentChunkFooter)
		if len(rest) < wantTotal {
			return fmt.Errorf("truncated chunk body/footer: declared body length %d needs %d total bytes, only %d left (torn read)", n, wantTotal, len(rest))
		}

		body := rest[contentChunkHeaderLen : contentChunkHeaderLen+n]
		for _, b := range body {
			if b != 'Z' {
				return fmt.Errorf("corrupted chunk body: expected all 'Z' bytes, found %q within declared body of length %d (torn read)", b, n)
			}
		}

		footer := rest[contentChunkHeaderLen+n : wantTotal]
		if string(footer) != contentChunkFooter {
			return fmt.Errorf("corrupted chunk footer: want %q, got %q (torn read)", contentChunkFooter, footer)
		}

		rest = rest[wantTotal:]
	}

	return nil
}

// TestContentAppendConcurrentRead covers subtask 4.5.5.3: a -race test that
// interleaves ContentStore.Append and ContentStore.Read goroutines against
// the SAME fileID, empirically pinning down ContentStore's no-torn-read
// guarantee (writeContentFile's write-temp-then-rename technique, and
// Append's content-write-ordered-before-cat.Put sequencing -- see
// content.go's ContentStore/Read/Append doc comments) against future
// refactors.
//
// This is deliberately a statistical/iteration-based race test, not a
// hook-forced deterministic one (contrast with engine/btree's
// optimisticReadHook/crabRetryHook seams): the guarantee under test is an
// OS/filesystem-level atomicity property of os.Rename exercised across many
// independent Append calls, not one narrow internal multi-step algorithm
// window the way btree's latch-crabbing retries are, so there is no single
// point a hook would usefully pin that many concurrent iterations under
// -race don't already exercise directly. This mirrors
// TestContentAppendConcurrentSameFileID's own docstring precedent, which
// likewise settles for "just the logical outcome" over a hook-based
// reproduction for a similar concurrency guarantee in this same file.
//
// Torn-read detection is done via parseContentChunks against
// self-describing chunks (see makeContentChunk), deliberately avoiding any
// external cross-goroutine "which Append committed when" bookkeeping in the
// test itself: bookkeeping done AFTER cs.Append returns (as it necessarily
// would be, from the calling goroutine) would itself race against
// concurrent readers that can already observe the just-committed content,
// which would produce false failures unrelated to the guarantee under test.
// The content-write-before-cat.Put half of the guarantee is instead checked
// via each reader observing cat.Get's SizeBytes immediately before its Read
// and asserting the read content is never shorter than that just-observed
// size (sizes are monotonically non-decreasing across Appends).
func TestContentAppendConcurrentRead(t *testing.T) {
	cs, cat, _ := newTestContentStore(t)

	const fileID = uint64(55)
	initial := []byte("# Concurrent Doc\n\n")
	rec := testContentRecord(fileID)
	rec.SizeBytes = uint64(len(initial))
	if _, err := cs.Create(rec, initial); err != nil {
		t.Fatalf("Create: %v", err)
	}

	const numChunks = 240
	const numWriters = 6
	const numReaders = 8

	chunks := make([][]byte, numChunks)
	for i := range chunks {
		chunks[i] = makeContentChunk(i)
	}

	var nextChunkIdx int32 // atomic work-stealing cursor into chunks

	var writersWG sync.WaitGroup
	for w := 0; w < numWriters; w++ {
		writersWG.Add(1)
		go func() {
			defer writersWG.Done()
			for {
				i := int(atomic.AddInt32(&nextChunkIdx, 1)) - 1
				if i >= numChunks {
					return
				}
				if _, err := cs.Append(fileID, chunks[i]); err != nil {
					t.Errorf("Append(#%d): %v", i, err)
					return
				}
			}
		}()
	}

	stop := make(chan struct{})
	var readersWG sync.WaitGroup
	for r := 0; r < numReaders; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}

				recBefore, err := cat.Get(fileID)
				if err != nil {
					t.Errorf("cat.Get: %v", err)
					return
				}

				content, err := cs.Read(fileID)
				if err != nil {
					t.Errorf("Read: %v", err)
					return
				}

				if uint64(len(content)) < recBefore.SizeBytes {
					t.Errorf("ordering violated: Read observed %d content bytes, smaller than catalog SizeBytes %d seen just before the read (content write must be ordered strictly before cat.Put becomes visible)", len(content), recBefore.SizeBytes)
					return
				}

				if err := parseContentChunks(content, initial); err != nil {
					t.Errorf("torn read detected: %v", err)
					return
				}
			}
		}()
	}

	writersWG.Wait()
	close(stop)
	readersWG.Wait()

	finalRec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("final cat.Get: %v", err)
	}
	finalContent, err := cs.Read(fileID)
	if err != nil {
		t.Fatalf("final Read: %v", err)
	}
	if uint64(len(finalContent)) != finalRec.SizeBytes {
		t.Fatalf("final content length = %d, want catalog SizeBytes %d", len(finalContent), finalRec.SizeBytes)
	}
	if err := parseContentChunks(finalContent, initial); err != nil {
		t.Fatalf("final content failed to parse as well-formed chunks: %v", err)
	}
}
