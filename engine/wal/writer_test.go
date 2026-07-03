package wal

import (
	"bytes"
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
