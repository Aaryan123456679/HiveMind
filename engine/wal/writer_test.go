package wal

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// listSegmentFiles returns the sorted (by segment number) list of segment
// file paths present in dir.
func listSegmentFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}

	sort.Strings(paths) // "wal-0.log" < "wal-1.log" < ... lexically matches numeric order for this test's range
	return paths
}

// TestSegmentWriter is the subtask's required test: write enough records to
// force rotation, then assert segment boundaries and record integrity.
func TestSegmentWriter(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 200
	w, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	const numRecords = 60
	var appended [][]byte
	for i := 0; i < numRecords; i++ {
		payload := []byte(fmt.Sprintf("record-%03d-payload-data", i))
		if _, err := w.Append(payload); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
		appended = append(appended, payload)
	}

	segFiles := listSegmentFiles(t, dir)
	if len(segFiles) < 3 {
		t.Fatalf("expected at least 3 segments to force >=2 rotations, got %d: %v", len(segFiles), segFiles)
	}

	// Each segment file (except it's fine for the final, still-open segment
	// to be smaller) must never exceed maxSegmentBytes: rotation must have
	// happened at the right boundary, not "grew unbounded then rotated
	// late".
	var allPayloads [][]byte
	for _, path := range segFiles {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s): %v", path, err)
		}
		if info.Size() > maxSegmentBytes {
			t.Errorf("segment %s size %d exceeds maxSegmentBytes %d", path, info.Size(), maxSegmentBytes)
		}

		// Parsing must succeed end-to-end using only this segment's own
		// bytes: if a record were split across the segment boundary,
		// ReadSegment would fail here with a truncation/corruption error.
		records, err := ReadSegment(path)
		if err != nil {
			t.Fatalf("ReadSegment(%s): %v (a split/torn record would surface here)", path, err)
		}
		allPayloads = append(allPayloads, records...)
	}

	if len(allPayloads) != len(appended) {
		t.Fatalf("total records parsed across segments = %d, want %d", len(allPayloads), len(appended))
	}
	for i := range appended {
		if !bytes.Equal(allPayloads[i], appended[i]) {
			t.Errorf("record %d mismatch: got %q, want %q", i, allPayloads[i], appended[i])
		}
	}
}

// TestAppendOversizedRecordHardErrors verifies the repo's established
// hard-error-not-truncate idiom: a record that can never fit within
// maxSegmentBytes must be rejected outright, never split or truncated.
func TestAppendOversizedRecordHardErrors(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 32
	w, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	oversized := make([]byte, maxSegmentBytes) // header alone is 8 bytes, so header+payload > maxSegmentBytes
	if _, err := w.Append(oversized); err == nil {
		t.Fatalf("Append of oversized record unexpectedly succeeded")
	}

	// A well-fitting record afterward must still succeed normally.
	small := []byte("ok")
	if _, err := w.Append(small); err != nil {
		t.Fatalf("Append(small) after rejected oversized record: %v", err)
	}

	records, err := ReadSegment(segmentPath(dir, 0))
	if err != nil {
		t.Fatalf("ReadSegment: %v", err)
	}
	if len(records) != 1 || !bytes.Equal(records[0], small) {
		t.Fatalf("segment contents = %v, want exactly [%q] (oversized record must not have been partially written)", records, small)
	}
}

// TestOpenWriterResumesExistingSegments verifies that reopening a Writer
// against a non-empty WAL directory continues numbering/appending correctly
// rather than clobbering existing segments.
func TestOpenWriterResumesExistingSegments(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 64

	w1, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter (first): %v", err)
	}
	first := []byte("first-record")
	if _, err := w1.Append(first); err != nil {
		t.Fatalf("Append(first): %v", err)
	}
	if err := w1.Close(); err != nil {
		t.Fatalf("Close (first): %v", err)
	}

	w2, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter (second/resume): %v", err)
	}
	defer w2.Close()

	if got := w2.SegmentNum(); got != 0 {
		t.Fatalf("resumed SegmentNum() = %d, want 0 (only one segment existed)", got)
	}

	second := []byte("second-record")
	if _, err := w2.Append(second); err != nil {
		t.Fatalf("Append(second): %v", err)
	}

	records, err := ReadSegment(segmentPath(dir, 0))
	if err != nil {
		t.Fatalf("ReadSegment: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("segment 0 has %d records, want 2 (resume must append after existing data, not overwrite it)", len(records))
	}
	if !bytes.Equal(records[0], first) {
		t.Errorf("record 0 = %q, want %q (resume must not clobber existing data)", records[0], first)
	}
	if !bytes.Equal(records[1], second) {
		t.Errorf("record 1 = %q, want %q", records[1], second)
	}
}

// TestOpenWriterResumeTornTailDiscardsAndTruncates closes the gap flagged in
// subtask 1.3.1's verification (regression.jsonl): OpenWriter's resume path
// previously did not validate a resumed segment's tail for torn/incomplete
// records at all. It simulates a crash mid-Append by hand-truncating a
// segment file so its last record is incomplete (both the
// truncated-header and truncated-payload shapes), then asserts:
//   - OpenWriter does not panic or error on the resumed, torn segment;
//   - the torn tail is physically discarded (the file is truncated to the
//     last valid record boundary) rather than left in place to corrupt a
//     later append;
//   - Writer.Offset() reports the post-truncation size, and a subsequent
//     Append lands immediately after the last valid record, producing a
//     segment that parses cleanly end-to-end with no torn/garbage bytes in
//     the middle.
func TestOpenWriterResumeTornTailDiscardsAndTruncates(t *testing.T) {
	t.Run("torn payload (header intact, payload cut short)", func(t *testing.T) {
		dir := t.TempDir()

		const maxSegmentBytes = 4096
		w, err := OpenWriter(dir, maxSegmentBytes)
		if err != nil {
			t.Fatalf("OpenWriter: %v", err)
		}
		first := []byte("valid-record-before-crash")
		if _, err := w.Append(first); err != nil {
			t.Fatalf("Append(first): %v", err)
		}
		validSize := w.Offset()
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		// Simulate a crash mid-Append: append a header claiming a payload
		// that is never fully written (only part of the declared payload
		// bytes make it to disk before the "crash").
		path := segmentPath(dir, 0)
		f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		var header [recordHeaderSize]byte
		binary.LittleEndian.PutUint32(header[offRecordLength:], 100) // claims 100 payload bytes
		binary.LittleEndian.PutUint32(header[offRecordCRC:], 0xDEADBEEF)
		if _, err := f.Write(header[:]); err != nil {
			t.Fatalf("writing torn header: %v", err)
		}
		if _, err := f.Write([]byte("only-a-few-bytes")); err != nil { // far fewer than 100
			t.Fatalf("writing torn payload: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("Close (torn writer): %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.Size() <= validSize {
			t.Fatalf("test setup did not actually grow the file past the valid record: size=%d, validSize=%d", info.Size(), validSize)
		}

		w2, err := OpenWriter(dir, maxSegmentBytes)
		if err != nil {
			t.Fatalf("OpenWriter on torn segment: got error %v, want a clean resume with the torn tail discarded", err)
		}
		defer w2.Close()

		if got := w2.Offset(); got != validSize {
			t.Fatalf("Offset() after resuming torn segment = %d, want %d (post-truncation size)", got, validSize)
		}

		info2, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat after resume: %v", err)
		}
		if info2.Size() != validSize {
			t.Fatalf("segment file size after resume = %d, want %d (torn tail must be physically truncated away)", info2.Size(), validSize)
		}

		second := []byte("valid-record-after-resume")
		if _, err := w2.Append(second); err != nil {
			t.Fatalf("Append(second) after resume: %v", err)
		}

		records, err := ReadSegment(path)
		if err != nil {
			t.Fatalf("ReadSegment after resume+append: %v (the torn tail must not have corrupted subsequent parsing)", err)
		}
		if len(records) != 2 || !bytes.Equal(records[0], first) || !bytes.Equal(records[1], second) {
			t.Fatalf("segment contents after resume = %v, want exactly [%q, %q]", records, first, second)
		}
	})

	t.Run("torn header (fewer than recordHeaderSize bytes trailing)", func(t *testing.T) {
		dir := t.TempDir()

		const maxSegmentBytes = 4096
		w, err := OpenWriter(dir, maxSegmentBytes)
		if err != nil {
			t.Fatalf("OpenWriter: %v", err)
		}
		first := []byte("valid-record-before-crash")
		if _, err := w.Append(first); err != nil {
			t.Fatalf("Append(first): %v", err)
		}
		validSize := w.Offset()
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		path := segmentPath(dir, 0)
		f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		// Crash happened while writing the header itself: only 3 of the 8
		// header bytes made it to disk.
		if _, err := f.Write([]byte{0x01, 0x02, 0x03}); err != nil {
			t.Fatalf("writing torn header bytes: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("Close (torn writer): %v", err)
		}

		w2, err := OpenWriter(dir, maxSegmentBytes)
		if err != nil {
			t.Fatalf("OpenWriter on torn-header segment: got error %v, want a clean resume with the torn header discarded", err)
		}
		defer w2.Close()

		if got := w2.Offset(); got != validSize {
			t.Fatalf("Offset() after resuming torn-header segment = %d, want %d", got, validSize)
		}

		records, err := ReadSegment(path)
		if err != nil {
			t.Fatalf("ReadSegment after resume: %v", err)
		}
		if len(records) != 1 || !bytes.Equal(records[0], first) {
			t.Fatalf("segment contents after resume = %v, want exactly [%q]", records, first)
		}
	})
}
