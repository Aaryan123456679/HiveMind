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

// TestExactBoundarySegmentRolloverResume closes the gap flagged in subtask
// 1.3.1's verification (regression.jsonl): no test previously exercised (a)
// a record whose total encoded size (header+payload) lands EXACTLY at
// maxSegmentBytes, asserting it stays in the current segment rather than
// triggering a spurious rotation (Append's rotate condition is a strict
// w.size+total > w.maxSegmentBytes, so an exact-equal total must NOT
// rotate), and (b) OpenWriter resuming with MORE THAN ONE pre-existing
// segment file present, asserting it selects the highest-numbered segment
// (not segment 0, and not a new one) and continues appending from its
// correctly restored size.
func TestExactBoundarySegmentRolloverResume(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 64 // recordHeaderSize (8) + 24-byte payloads divides evenly for an exact-boundary total

	w1, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter (first): %v", err)
	}

	// record1 + record2 each total recordHeaderSize+24 = 32 bytes, so after
	// both, w.size == 64 == maxSegmentBytes exactly: the exact-boundary case.
	record1 := bytes.Repeat([]byte("a"), 24)
	record2 := bytes.Repeat([]byte("b"), 24)
	if _, err := w1.Append(record1); err != nil {
		t.Fatalf("Append(record1): %v", err)
	}
	if _, err := w1.Append(record2); err != nil {
		t.Fatalf("Append(record2): %v", err)
	}

	if got := w1.SegmentNum(); got != 0 {
		t.Fatalf("after two exact-fitting records, SegmentNum() = %d, want 0 (no rotation should have happened yet)", got)
	}
	if got := w1.Offset(); got != maxSegmentBytes {
		t.Fatalf("after two exact-fitting records, Offset() = %d, want exactly maxSegmentBytes (%d)", got, int64(maxSegmentBytes))
	}

	// A third record must rotate into segment 1, since segment 0 is already
	// exactly full (size(64) + total(> 0) > maxSegmentBytes(64)).
	record3 := []byte("seg1-first")
	if _, err := w1.Append(record3); err != nil {
		t.Fatalf("Append(record3): %v", err)
	}
	if got := w1.SegmentNum(); got != 1 {
		t.Fatalf("after third record, SegmentNum() = %d, want 1 (exact-boundary-full segment 0 must rotate, not overflow)", got)
	}

	if err := w1.Close(); err != nil {
		t.Fatalf("Close (first writer): %v", err)
	}

	// Confirm segment 0's on-disk size is exactly maxSegmentBytes (the
	// boundary record was NOT split or spilled into a new segment) and that
	// it parses back to exactly [record1, record2].
	seg0Path := segmentPath(dir, 0)
	info, err := os.Stat(seg0Path)
	if err != nil {
		t.Fatalf("Stat(segment 0): %v", err)
	}
	if info.Size() != maxSegmentBytes {
		t.Fatalf("segment 0 on-disk size = %d, want exactly maxSegmentBytes (%d)", info.Size(), int64(maxSegmentBytes))
	}
	seg0Records, err := ReadSegment(seg0Path)
	if err != nil {
		t.Fatalf("ReadSegment(segment 0): %v", err)
	}
	if len(seg0Records) != 2 || !bytes.Equal(seg0Records[0], record1) || !bytes.Equal(seg0Records[1], record2) {
		t.Fatalf("segment 0 records = %v, want exactly [record1, record2] (boundary record must not have rotated prematurely)", seg0Records)
	}

	// Precondition for the resume half of this test: more than one segment
	// file must actually be present on disk before we resume.
	segFilesBeforeResume := listSegmentFiles(t, dir)
	if len(segFilesBeforeResume) != 2 {
		t.Fatalf("expected exactly 2 pre-existing segment files before resume, got %d: %v", len(segFilesBeforeResume), segFilesBeforeResume)
	}

	// Resume: OpenWriter must select segment 1 (the highest-numbered
	// existing segment), not segment 0 and not a freshly rotated segment 2,
	// and must restore its correct current size.
	w2, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter (resume): %v", err)
	}
	defer w2.Close()

	if got := w2.SegmentNum(); got != 1 {
		t.Fatalf("resumed SegmentNum() = %d, want 1 (highest of the 2 pre-existing segments)", got)
	}
	wantResumeOffset := int64(recordHeaderSize + len(record3))
	if got := w2.Offset(); got != wantResumeOffset {
		t.Fatalf("resumed Offset() = %d, want %d (segment 1's restored size)", got, wantResumeOffset)
	}

	record4 := []byte("seg1-second")
	if _, err := w2.Append(record4); err != nil {
		t.Fatalf("Append(record4): %v", err)
	}
	if got := w2.SegmentNum(); got != 1 {
		t.Fatalf("after resumed append, SegmentNum() = %d, want 1 (record4 fits well within maxSegmentBytes, no rotation expected)", got)
	}

	// Segment 0 must remain completely untouched by the resume+append.
	seg0RecordsAfter, err := ReadSegment(seg0Path)
	if err != nil {
		t.Fatalf("ReadSegment(segment 0) after resume: %v", err)
	}
	if len(seg0RecordsAfter) != 2 || !bytes.Equal(seg0RecordsAfter[0], record1) || !bytes.Equal(seg0RecordsAfter[1], record2) {
		t.Fatalf("segment 0 records after resume = %v, want unchanged [record1, record2]", seg0RecordsAfter)
	}

	// Segment 1 must now contain record3 (pre-existing) followed by record4
	// (appended after resume), proving the resumed writer continued
	// appending onto the correct (highest-numbered) segment rather than
	// overwriting it or starting a new one.
	seg1Records, err := ReadSegment(segmentPath(dir, 1))
	if err != nil {
		t.Fatalf("ReadSegment(segment 1): %v", err)
	}
	if len(seg1Records) != 2 || !bytes.Equal(seg1Records[0], record3) || !bytes.Equal(seg1Records[1], record4) {
		t.Fatalf("segment 1 records = %v, want exactly [record3, record4]", seg1Records)
	}

	// No third segment file should have been created.
	segFilesAfter := listSegmentFiles(t, dir)
	if len(segFilesAfter) != 2 {
		t.Fatalf("expected exactly 2 segment files after resume+append, got %d: %v", len(segFilesAfter), segFilesAfter)
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

// TestReadSegmentCRCCorruption exercises ReadSegment's own CRC-corruption
// error path directly (fix-cycle for subtask 4.5.14.4 / verification run
// 150-verification, findings 1 and 2). Unlike TestReplayCRCCorruption
// (recovery_test.go), which drives the same corrupted segment through
// Replay/readSegmentFrom, this test calls ReadSegment(path) itself, since
// the 4.5.14.4 refactor (commit 3c7ad8f) made ReadSegment delegate to
// readSegmentFrom and no existing test exercised ReadSegment's own call site
// against a CRC-corrupted (non-torn) record. It also locks in ReadSegment's
// nil-records-on-error contract: readSegmentFrom returns the partial
// records parsed before the corrupt one (needed by Replay), but ReadSegment
// must normalize that to nil records alongside the error, matching its
// pre-refactor (3c7ad8f^) behavior.
func TestReadSegmentCRCCorruption(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 4096
	w, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}

	var offsets []int64
	const numRecords = 8
	for i := 0; i < numRecords; i++ {
		rec := NewCatalogPutRecord(uint64(i), []byte(fmt.Sprintf("value-%03d", i)))
		offset, err := w.Append(rec.Encode())
		if err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
		offsets = append(offsets, offset)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Corrupt one payload byte in a record that is NOT the last one, so the
	// failure is unambiguously a CRC mismatch rather than a torn tail (see
	// TestReplayCRCCorruption for the same corruption-injection technique).
	const corruptIdx = 4
	path := segmentPath(dir, 0)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	payloadStart := offsets[corruptIdx] + recordHeaderSize
	original := data[payloadStart]
	data[payloadStart] ^= 0xFF
	if data[payloadStart] == original {
		t.Fatalf("test setup failed to actually change the byte at offset %d", payloadStart)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile (corrupted): %v", err)
	}

	// Sanity: confirm the flipped byte actually breaks this record's CRC as
	// parsed by the package's own parser, so the assertion below exercises a
	// genuine CRC failure rather than an unrelated one.
	if _, _, _, perr := parseSegmentRecords(data, int(offsets[corruptIdx])); perr == nil {
		t.Fatalf("test setup: corrupting one payload byte at offset %d did not trigger a CRC mismatch as expected", payloadStart)
	}

	records, err := ReadSegment(path)
	if err == nil {
		t.Fatalf("ReadSegment: got nil error, want a hard CRC error for the corrupted record at index %d", corruptIdx)
	}
	if records != nil {
		t.Fatalf("ReadSegment: got records = %v alongside a non-nil error, want nil records (ReadSegment must not propagate readSegmentFrom's partial records)", records)
	}
}
