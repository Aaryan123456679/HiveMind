package wal

import (
	"fmt"
	"testing"
)

// TestCheckpointManifest is the subtask's required test: write records across
// enough segments to force at least 2 rotations, checkpoint at a pointer
// partway through, and confirm manifest.json round-trips correctly and old
// segments are correctly identified as archivable.
func TestCheckpointManifest(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 128
	w, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer w.Close()

	const numRecords = 40
	var checkpointSegment uint64
	var checkpointOffset int64
	haveCheckpointPoint := false

	for i := 0; i < numRecords; i++ {
		rec := NewCatalogPutRecord(uint64(i), []byte(fmt.Sprintf("catalog-record-%03d", i)))
		payload := rec.Encode()

		offset, err := w.Append(payload)
		if err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}

		// Capture a checkpoint pointer partway through (after the first
		// rotation has definitely happened, so the checkpoint's segment is
		// not segment 0 and there is at least one earlier segment to be
		// archivable).
		if !haveCheckpointPoint && w.SegmentNum() >= 1 {
			checkpointSegment = uint64(w.SegmentNum())
			checkpointOffset = offset
			haveCheckpointPoint = true
		}
	}

	if !haveCheckpointPoint {
		t.Fatalf("test setup did not force enough rotations to reach segment >= 1; got final SegmentNum=%d", w.SegmentNum())
	}
	if w.SegmentNum() <= int(checkpointSegment) {
		t.Fatalf("test setup did not continue writing past the checkpoint's segment (checkpoint segment %d, final segment %d); need a later segment to prove it is excluded from archivable results", checkpointSegment, w.SegmentNum())
	}

	if err := Checkpoint(dir, checkpointSegment, checkpointOffset); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	gotSegment, gotOffset, found, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if !found {
		t.Fatalf("LoadCheckpoint found=false, want true after Checkpoint was called")
	}
	if gotSegment != checkpointSegment {
		t.Errorf("LoadCheckpoint segmentNumber = %d, want %d", gotSegment, checkpointSegment)
	}
	if gotOffset != checkpointOffset {
		t.Errorf("LoadCheckpoint offsetInSegment = %d, want %d", gotOffset, checkpointOffset)
	}

	archivable, err := ArchivableSegments(dir, checkpointSegment)
	if err != nil {
		t.Fatalf("ArchivableSegments: %v", err)
	}

	wantPaths := make([]string, 0, checkpointSegment)
	for n := uint64(0); n < checkpointSegment; n++ {
		wantPaths = append(wantPaths, segmentPath(dir, int(n)))
	}

	if len(archivable) != len(wantPaths) {
		t.Fatalf("ArchivableSegments returned %d paths, want %d: got %v, want %v", len(archivable), len(wantPaths), archivable, wantPaths)
	}
	for i := range wantPaths {
		if archivable[i] != wantPaths[i] {
			t.Errorf("ArchivableSegments[%d] = %q, want %q", i, archivable[i], wantPaths[i])
		}
	}

	// Negative assertions: neither the checkpoint's own segment nor any
	// later segment must appear in the archivable list.
	checkpointOwnPath := segmentPath(dir, int(checkpointSegment))
	laterPath := segmentPath(dir, w.SegmentNum())
	for _, p := range archivable {
		if p == checkpointOwnPath {
			t.Errorf("ArchivableSegments included the checkpoint's own segment %q, want it excluded", p)
		}
		if p == laterPath {
			t.Errorf("ArchivableSegments included a segment %q newer than the checkpoint, want it excluded", p)
		}
	}
}

// TestLoadCheckpointNoManifest verifies that a WAL directory with no
// manifest.json yet (a fresh WAL, nothing checkpointed) reports found=false
// with a nil error, not an error condition.
func TestLoadCheckpointNoManifest(t *testing.T) {
	dir := t.TempDir()

	segmentNumber, offsetInSegment, found, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint on fresh dir: unexpected error: %v", err)
	}
	if found {
		t.Fatalf("LoadCheckpoint found=true on a directory with no manifest.json, want false")
	}
	if segmentNumber != 0 || offsetInSegment != 0 {
		t.Errorf("LoadCheckpoint returned (%d, %d) with found=false, want zero values", segmentNumber, offsetInSegment)
	}
}
