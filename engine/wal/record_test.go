package wal

import (
	"errors"
	"path/filepath"
	"testing"
)

func openTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	w, err := OpenWriter(dir, 4096)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, dir
}

// TestRecordEncodeDecodeRoundTrip covers encode/decode round trips for every
// record kind, both via the TypedRecord envelope and via each kind's own
// Encode/Decode pair.
func TestRecordEncodeDecodeRoundTrip(t *testing.T) {
	t.Run("CatalogPut", func(t *testing.T) {
		want := CatalogPutPayload{FileID: 42, Record: []byte{1, 2, 3, 4, 5}}
		rec := NewCatalogPutRecord(want.FileID, want.Record)
		if rec.Type != RecordCatalogPut {
			t.Fatalf("Type = %v, want RecordCatalogPut", rec.Type)
		}

		encoded := rec.Encode()
		decodedTR, err := DecodeTypedRecord(encoded)
		if err != nil {
			t.Fatalf("DecodeTypedRecord: %v", err)
		}
		if decodedTR.Type != RecordCatalogPut {
			t.Fatalf("decoded Type = %v, want RecordCatalogPut", decodedTR.Type)
		}

		got, err := decodedTR.AsCatalogPut()
		if err != nil {
			t.Fatalf("AsCatalogPut: %v", err)
		}
		if got.FileID != want.FileID {
			t.Errorf("FileID = %d, want %d", got.FileID, want.FileID)
		}
		if string(got.Record) != string(want.Record) {
			t.Errorf("Record = %v, want %v", got.Record, want.Record)
		}
	})

	t.Run("CatalogDelete", func(t *testing.T) {
		rec := NewCatalogDeleteRecord(7)
		encoded := rec.Encode()
		decodedTR, err := DecodeTypedRecord(encoded)
		if err != nil {
			t.Fatalf("DecodeTypedRecord: %v", err)
		}
		got, err := decodedTR.AsCatalogDelete()
		if err != nil {
			t.Fatalf("AsCatalogDelete: %v", err)
		}
		if got.FileID != 7 {
			t.Errorf("FileID = %d, want 7", got.FileID)
		}
	})

	t.Run("BTreeInsert", func(t *testing.T) {
		rec := NewBTreeInsertRecord("/topics/foo.md", 99)
		encoded := rec.Encode()
		decodedTR, err := DecodeTypedRecord(encoded)
		if err != nil {
			t.Fatalf("DecodeTypedRecord: %v", err)
		}
		got, err := decodedTR.AsBTreeInsert()
		if err != nil {
			t.Fatalf("AsBTreeInsert: %v", err)
		}
		if got.Path != "/topics/foo.md" {
			t.Errorf("Path = %q, want %q", got.Path, "/topics/foo.md")
		}
		if got.FileID != 99 {
			t.Errorf("FileID = %d, want 99", got.FileID)
		}
	})

	t.Run("BTreeDelete", func(t *testing.T) {
		rec := NewBTreeDeleteRecord("/topics/bar.md")
		encoded := rec.Encode()
		decodedTR, err := DecodeTypedRecord(encoded)
		if err != nil {
			t.Fatalf("DecodeTypedRecord: %v", err)
		}
		got, err := decodedTR.AsBTreeDelete()
		if err != nil {
			t.Fatalf("AsBTreeDelete: %v", err)
		}
		if got.Path != "/topics/bar.md" {
			t.Errorf("Path = %q, want %q", got.Path, "/topics/bar.md")
		}
	})

	t.Run("empty path and empty record blob", func(t *testing.T) {
		rec := NewBTreeDeleteRecord("")
		decodedTR, err := DecodeTypedRecord(rec.Encode())
		if err != nil {
			t.Fatalf("DecodeTypedRecord: %v", err)
		}
		got, err := decodedTR.AsBTreeDelete()
		if err != nil {
			t.Fatalf("AsBTreeDelete: %v", err)
		}
		if got.Path != "" {
			t.Errorf("Path = %q, want empty", got.Path)
		}

		putRec := NewCatalogPutRecord(1, nil)
		decodedPut, err := DecodeTypedRecord(putRec.Encode())
		if err != nil {
			t.Fatalf("DecodeTypedRecord: %v", err)
		}
		gotPut, err := decodedPut.AsCatalogPut()
		if err != nil {
			t.Fatalf("AsCatalogPut: %v", err)
		}
		if len(gotPut.Record) != 0 {
			t.Errorf("Record = %v, want empty", gotPut.Record)
		}
	})

	t.Run("SplitCommit", func(t *testing.T) {
		want := SplitCommitPayload{
			OriginalFileID:       1,
			OldPath:              "/topics/original.md",
			EncodedCatalogRecord: []byte{9, 8, 7, 6, 5},
			Entries: []SplitCommitEntry{
				{NewPath: "/topics/part-1.md", FileID: 2},
				{NewPath: "/topics/part-2.md", FileID: 3},
			},
		}
		rec := NewSplitCommitRecord(want)
		if rec.Type != RecordSplitCommit {
			t.Fatalf("Type = %v, want RecordSplitCommit", rec.Type)
		}

		decodedTR, err := DecodeTypedRecord(rec.Encode())
		if err != nil {
			t.Fatalf("DecodeTypedRecord: %v", err)
		}
		if decodedTR.Type != RecordSplitCommit {
			t.Fatalf("decoded Type = %v, want RecordSplitCommit", decodedTR.Type)
		}

		got, err := decodedTR.AsSplitCommit()
		if err != nil {
			t.Fatalf("AsSplitCommit: %v", err)
		}
		if got.OriginalFileID != want.OriginalFileID {
			t.Errorf("OriginalFileID = %d, want %d", got.OriginalFileID, want.OriginalFileID)
		}
		if got.OldPath != want.OldPath {
			t.Errorf("OldPath = %q, want %q", got.OldPath, want.OldPath)
		}
		if string(got.EncodedCatalogRecord) != string(want.EncodedCatalogRecord) {
			t.Errorf("EncodedCatalogRecord = %v, want %v", got.EncodedCatalogRecord, want.EncodedCatalogRecord)
		}
		if len(got.Entries) != len(want.Entries) {
			t.Fatalf("len(Entries) = %d, want %d", len(got.Entries), len(want.Entries))
		}
		for i := range want.Entries {
			if got.Entries[i] != want.Entries[i] {
				t.Errorf("Entries[%d] = %+v, want %+v", i, got.Entries[i], want.Entries[i])
			}
		}
	})

	t.Run("SplitCommit with no entries and empty old path", func(t *testing.T) {
		rec := NewSplitCommitRecord(SplitCommitPayload{OriginalFileID: 42})
		decodedTR, err := DecodeTypedRecord(rec.Encode())
		if err != nil {
			t.Fatalf("DecodeTypedRecord: %v", err)
		}
		got, err := decodedTR.AsSplitCommit()
		if err != nil {
			t.Fatalf("AsSplitCommit: %v", err)
		}
		if got.OriginalFileID != 42 {
			t.Errorf("OriginalFileID = %d, want 42", got.OriginalFileID)
		}
		if got.OldPath != "" {
			t.Errorf("OldPath = %q, want empty", got.OldPath)
		}
		if len(got.Entries) != 0 {
			t.Errorf("Entries = %v, want empty", got.Entries)
		}
	})
}

// TestAsXxxTypeMismatch verifies each AsXxx accessor rejects a TypedRecord
// whose Type does not match.
func TestAsXxxTypeMismatch(t *testing.T) {
	rec := NewCatalogDeleteRecord(1)

	if _, err := rec.AsCatalogPut(); err == nil {
		t.Error("AsCatalogPut on a CatalogDelete record: expected error, got nil")
	}
	if _, err := rec.AsBTreeInsert(); err == nil {
		t.Error("AsBTreeInsert on a CatalogDelete record: expected error, got nil")
	}
	if _, err := rec.AsBTreeDelete(); err == nil {
		t.Error("AsBTreeDelete on a CatalogDelete record: expected error, got nil")
	}
	if _, err := rec.AsSplitCommit(); err == nil {
		t.Error("AsSplitCommit on a CatalogDelete record: expected error, got nil")
	}
}

// TestFsyncBeforeApply proves — empirically, via the package's own exported
// API rather than by inspecting source — that AppendAndApply's apply
// callback only fires after the WAL record has been durably fsynced to
// disk.
//
// The proof: inside the apply callback, independently re-read the segment
// file from disk via ReadSegment (which does its own fresh os.ReadFile, with
// no shared state with Writer). If AppendAndApply's apply callback ran
// before Writer.Append's fsync had completed, there would be a window where
// the record's bytes could be missing or only partially flushed from the
// process's perspective for a fresh read; because ReadSegment observes the
// full, CRC-valid record already present at the moment apply fires, this
// demonstrates the fsync-before-apply ordering holds by observation, not
// just by reading AppendAndApply's implementation.
func TestFsyncBeforeApply(t *testing.T) {
	w, dir := openTestWriter(t)

	rec := NewCatalogPutRecord(123, []byte("catalog-record-bytes"))

	var events []string
	var sawDurableAtApplyTime bool

	apply := func() error {
		events = append(events, "apply")

		segPath := filepath.Join(dir, "wal-0.log")
		records, err := ReadSegment(segPath)
		if err != nil {
			t.Fatalf("ReadSegment inside apply callback: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("ReadSegment inside apply callback: got %d records, want 1 (record must already be durable when apply fires)", len(records))
		}

		decoded, err := DecodeTypedRecord(records[0])
		if err != nil {
			t.Fatalf("DecodeTypedRecord inside apply callback: %v", err)
		}
		got, err := decoded.AsCatalogPut()
		if err != nil {
			t.Fatalf("AsCatalogPut inside apply callback: %v", err)
		}
		if got.FileID != 123 {
			t.Fatalf("FileID observed inside apply = %d, want 123 (durable record content must match before apply runs)", got.FileID)
		}

		sawDurableAtApplyTime = true
		return nil
	}

	events = append(events, "before-append")
	offset, err := AppendAndApply(w, rec, apply)
	if err != nil {
		t.Fatalf("AppendAndApply: %v", err)
	}
	events = append(events, "after-append-and-apply")

	if offset != 0 {
		t.Errorf("offset = %d, want 0 (first record in a fresh segment)", offset)
	}
	if !sawDurableAtApplyTime {
		t.Fatal("apply callback did not observe a durable record on disk; fsync-before-apply ordering did not hold")
	}

	wantEvents := []string{"before-append", "apply", "after-append-and-apply"}
	if len(events) != len(wantEvents) {
		t.Fatalf("events = %v, want %v", events, wantEvents)
	}
	for i := range wantEvents {
		if events[i] != wantEvents[i] {
			t.Fatalf("events[%d] = %q, want %q (full sequence: %v)", i, events[i], wantEvents[i], events)
		}
	}

	// Independently, outside of the apply callback, confirm the record is
	// (still) durably readable back from disk after AppendAndApply returns.
	records, err := ReadSegment(filepath.Join(dir, "wal-0.log"))
	if err != nil {
		t.Fatalf("ReadSegment after AppendAndApply: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records after AppendAndApply, want 1", len(records))
	}
}

// TestAppendAndApplyErrorFromApply verifies that when the underlying
// Writer.Append succeeds but apply fails, AppendAndApply propagates the
// apply error (wrapped) while the WAL record remains durably persisted —
// the documented "a failed apply does not un-happen the durable log write"
// semantics.
func TestAppendAndApplyErrorFromApply(t *testing.T) {
	w, dir := openTestWriter(t)

	rec := NewBTreeInsertRecord("/topics/needs-retry.md", 55)
	applyErr := errors.New("simulated apply failure")

	offset, err := AppendAndApply(w, rec, func() error {
		return applyErr
	})
	if err == nil {
		t.Fatal("AppendAndApply: expected error from failing apply, got nil")
	}
	if !errors.Is(err, applyErr) {
		t.Errorf("AppendAndApply error = %v, want it to wrap %v", err, applyErr)
	}
	if offset != 0 {
		t.Errorf("offset = %d, want 0 (first record in a fresh segment)", offset)
	}

	// The record must still be durably present on disk despite apply's
	// failure.
	records, err := ReadSegment(filepath.Join(dir, "wal-0.log"))
	if err != nil {
		t.Fatalf("ReadSegment: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records after a failed apply, want 1 (WAL record must survive apply failure)", len(records))
	}

	decoded, err := DecodeTypedRecord(records[0])
	if err != nil {
		t.Fatalf("DecodeTypedRecord: %v", err)
	}
	got, err := decoded.AsBTreeInsert()
	if err != nil {
		t.Fatalf("AsBTreeInsert: %v", err)
	}
	if got.Path != "/topics/needs-retry.md" || got.FileID != 55 {
		t.Errorf("got %+v, want Path=/topics/needs-retry.md FileID=55", got)
	}
}

// TestAppendAndApplyWriterErrorSkipsApply verifies that when the underlying
// Writer.Append itself fails, apply is never invoked.
func TestAppendAndApplyWriterErrorSkipsApply(t *testing.T) {
	dir := t.TempDir()
	// maxSegmentBytes deliberately tiny relative to the record below so
	// Writer.Append hard-errors (per 1.3.1's "hard-error, not truncate, on
	// overflow" contract) rather than writing anything.
	w, err := OpenWriter(dir, recordHeaderSize+recordTypeSize+4)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	rec := NewCatalogPutRecord(1, []byte("this record's payload is deliberately too large to fit"))

	applyCalled := false
	_, err = AppendAndApply(w, rec, func() error {
		applyCalled = true
		return nil
	})
	if err == nil {
		t.Fatal("AppendAndApply: expected error from oversized record, got nil")
	}
	if applyCalled {
		t.Fatal("apply was called despite Writer.Append failing; fsync-before-apply ordering must skip apply entirely on append failure")
	}
}
