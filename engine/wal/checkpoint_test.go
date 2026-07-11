package wal

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
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

// TestCheckpointManifestRoundTripTableDriven is subtask 4.5.14.3's required
// test (issue #52, originating from the 2026-07-04 040-verification
// regression note on subtask 1.3.3): TestCheckpointManifest above only ever
// empirically exercises Checkpoint+LoadCheckpoint round-trip fidelity for a
// single (segmentNumber, offsetInSegment) pair. This test exercises several
// distinct pairs table-driven, explicitly including offset=0 (both alone and
// paired with segment 0), and a large segment/offset pair, each written to
// and read back from its own fresh WAL directory.
func TestCheckpointManifestRoundTripTableDriven(t *testing.T) {
	tests := []struct {
		name            string
		segmentNumber   uint64
		offsetInSegment int64
	}{
		{name: "segment0_offset0", segmentNumber: 0, offsetInSegment: 0},
		{name: "segment0_offsetNonZero", segmentNumber: 0, offsetInSegment: 128},
		{name: "segmentNonZero_offset0", segmentNumber: 1, offsetInSegment: 0},
		{name: "midRangePair", segmentNumber: 5, offsetInSegment: 4096},
		{name: "largeSegmentAndOffset", segmentNumber: uint64(math.MaxUint32), offsetInSegment: int64(math.MaxInt32)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			if err := Checkpoint(dir, tc.segmentNumber, tc.offsetInSegment); err != nil {
				t.Fatalf("Checkpoint(%d, %d): %v", tc.segmentNumber, tc.offsetInSegment, err)
			}

			gotSegment, gotOffset, found, err := LoadCheckpoint(dir)
			if err != nil {
				t.Fatalf("LoadCheckpoint: %v", err)
			}
			if !found {
				t.Fatalf("LoadCheckpoint found=false, want true after Checkpoint(%d, %d)", tc.segmentNumber, tc.offsetInSegment)
			}
			if gotSegment != tc.segmentNumber {
				t.Errorf("LoadCheckpoint segmentNumber = %d, want %d", gotSegment, tc.segmentNumber)
			}
			if gotOffset != tc.offsetInSegment {
				t.Errorf("LoadCheckpoint offsetInSegment = %d, want %d", gotOffset, tc.offsetInSegment)
			}
		})
	}
}

// TestCheckpointDoubleOverwrite is subtask 4.5.14.3's second required test
// (issue #52): calling Checkpoint twice in succession, with two distinct
// pointers, must cleanly overwrite manifest.json with the second pointer's
// values -- not merge, not corrupt, and not leave a stray manifest.json.tmp
// behind (Checkpoint's atomic temp-file+Sync+os.Rename idiom should consume
// the temp file into the final path on every call).
func TestCheckpointDoubleOverwrite(t *testing.T) {
	dir := t.TempDir()

	first := CheckpointPointer{SegmentNumber: 2, OffsetInSegment: 64}
	second := CheckpointPointer{SegmentNumber: 9, OffsetInSegment: 1024}

	if err := Checkpoint(dir, first.SegmentNumber, first.OffsetInSegment); err != nil {
		t.Fatalf("first Checkpoint(%d, %d): %v", first.SegmentNumber, first.OffsetInSegment, err)
	}

	tmpPath := filepath.Join(dir, manifestTempFileName)
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("after first Checkpoint, manifest.json.tmp still exists (stat err = %v), want it consumed by os.Rename", err)
	}

	if err := Checkpoint(dir, second.SegmentNumber, second.OffsetInSegment); err != nil {
		t.Fatalf("second Checkpoint(%d, %d): %v", second.SegmentNumber, second.OffsetInSegment, err)
	}

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("after second Checkpoint, manifest.json.tmp still exists (stat err = %v), want it consumed by os.Rename", err)
	}

	// LoadCheckpoint must report the second pointer's values, not the
	// first's and not some merge of the two.
	gotSegment, gotOffset, found, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if !found {
		t.Fatalf("LoadCheckpoint found=false, want true after two Checkpoint calls")
	}
	if gotSegment != second.SegmentNumber {
		t.Errorf("LoadCheckpoint segmentNumber = %d, want %d (the second Checkpoint's value, want no trace of first Checkpoint's %d)", gotSegment, second.SegmentNumber, first.SegmentNumber)
	}
	if gotOffset != second.OffsetInSegment {
		t.Errorf("LoadCheckpoint offsetInSegment = %d, want %d (the second Checkpoint's value, want no trace of first Checkpoint's %d)", gotOffset, second.OffsetInSegment, first.OffsetInSegment)
	}

	// Independently verify manifest.json on disk is a single, well-formed
	// JSON document with exactly the second pointer's values -- not a
	// concatenation/corruption of both writes, which LoadCheckpoint's
	// json.Unmarshal alone might tolerate or mask (e.g. if additional
	// trailing bytes happened not to break decoding of the first value).
	manifestPath := filepath.Join(dir, manifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest.json directly: %v", err)
	}

	var onDisk CheckpointPointer
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("manifest.json is not valid, well-formed JSON: %v (raw contents: %s)", err, data)
	}
	if onDisk != second {
		t.Errorf("manifest.json on disk decodes to %+v, want exactly the second pointer %+v (no corruption/merge)", onDisk, second)
	}

	// Ensure the decoder consumed the entire byte stream (no trailing
	// garbage/second concatenated document), by re-encoding through the
	// same MarshalIndent format Checkpoint uses and confirming byte-for-byte
	// equality with what is actually on disk.
	wantBytes, err := json.MarshalIndent(second, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent(second): %v", err)
	}
	if string(data) != string(wantBytes) {
		t.Errorf("manifest.json raw bytes = %q, want exactly %q (byte-for-byte, no leftover/corrupted content)", data, wantBytes)
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
