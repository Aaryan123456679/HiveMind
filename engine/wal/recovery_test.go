package wal

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"testing"
)

// appendedRecord tracks one record this test package appended directly, so
// assertions can be made about exactly which records Replay should and
// should not reapply.
type appendedRecord struct {
	segNum int
	offset int64
	rec    TypedRecord
}

// applyToState is a test-local fake "apply" implementation that mutates an
// in-memory map keyed by a string form of the mutated FileID/path, mirroring
// how a real caller would decode a TypedRecord's payload and mutate a live
// catalog/B+Tree store. This package intentionally does not wire Replay into
// engine/catalog or engine/btree (see recovery.go's doc comment and this
// run's architecture-discovery.md); a fake in-memory apply is the correct
// substitute for this subtask's own test, mirroring 1.3.2's
// TestFsyncBeforeApply injected-callback pattern.
func applyToState(state map[string]string, rec TypedRecord) error {
	switch rec.Type {
	case RecordCatalogPut:
		p, err := rec.AsCatalogPut()
		if err != nil {
			return err
		}
		state[strconv.FormatUint(p.FileID, 10)] = string(p.Record)
	case RecordCatalogDelete:
		p, err := rec.AsCatalogDelete()
		if err != nil {
			return err
		}
		delete(state, strconv.FormatUint(p.FileID, 10))
	case RecordBTreeInsert:
		p, err := rec.AsBTreeInsert()
		if err != nil {
			return err
		}
		state[p.Path] = strconv.FormatUint(p.FileID, 10)
	case RecordBTreeDelete:
		p, err := rec.AsBTreeDelete()
		if err != nil {
			return err
		}
		delete(state, p.Path)
	default:
		return fmt.Errorf("applyToState: unexpected record type %s", rec.Type)
	}
	return nil
}

func cloneState(state map[string]string) map[string]string {
	out := make(map[string]string, len(state))
	for k, v := range state {
		out[k] = v
	}
	return out
}

// TestRecoveryReplay is the subtask's required test (go test
// ./engine/wal/... -run TestRecoveryReplay): pre-populate a WAL directory
// with mutations spanning multiple segments, checkpoint partway through
// (leaving mutations past the checkpoint), run Replay, and assert:
//   - every record from the checkpoint's offset forward (its own segment and
//     all later segments) is replayed exactly once, in exact original order;
//   - records before the checkpoint pointer are not replayed;
//   - the final state produced by (state-as-of-checkpoint + replayed
//     records) matches the final state produced by applying every original
//     mutation directly, from scratch.
func TestRecoveryReplay(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 128
	w, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}

	const numRecords = 40
	var all []appendedRecord
	for i := 0; i < numRecords; i++ {
		var rec TypedRecord
		fileID := uint64(i / 3)
		if i%3 == 2 {
			rec = NewCatalogDeleteRecord(fileID)
		} else {
			rec = NewCatalogPutRecord(fileID, []byte(fmt.Sprintf("value-%03d", i)))
		}

		segNum := w.SegmentNum()
		offset, err := w.Append(rec.Encode())
		if err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
		all = append(all, appendedRecord{segNum: segNum, offset: offset, rec: rec})
	}

	// Pick a checkpoint partway through: after the first rotation (so the
	// checkpoint's segment is not segment 0), with more records written
	// after it in later segments.
	checkpointIdx := -1
	for idx, a := range all {
		if a.segNum >= 1 {
			checkpointIdx = idx
			break
		}
	}
	if checkpointIdx == -1 {
		t.Fatalf("test setup did not force enough rotations to reach segment >= 1; final SegmentNum=%d", w.SegmentNum())
	}
	if w.SegmentNum() <= all[checkpointIdx].segNum {
		t.Fatalf("test setup did not continue writing past the checkpoint's segment (checkpoint segment %d, final segment %d)", all[checkpointIdx].segNum, w.SegmentNum())
	}

	cp := all[checkpointIdx]
	if err := Checkpoint(dir, uint64(cp.segNum), cp.offset); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Writer.Close: %v", err)
	}

	var got []TypedRecord
	err = Replay(dir, func(rec TypedRecord) error {
		got = append(got, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	want := all[checkpointIdx:]
	if len(got) != len(want) {
		t.Fatalf("Replay called apply %d times, want %d (records from checkpoint index %d forward)", len(got), len(want), checkpointIdx)
	}
	for i := range want {
		if got[i].Type != want[i].rec.Type || !bytes.Equal(got[i].Payload, want[i].rec.Payload) {
			t.Errorf("replayed record %d = {%s, %x}, want {%s, %x}", i, got[i].Type, got[i].Payload, want[i].rec.Type, want[i].rec.Payload)
		}
	}

	// Negative assertion: nothing before the checkpoint index appears among
	// the replayed records (would indicate double-application).
	for i := 0; i < checkpointIdx; i++ {
		for _, g := range got {
			if g.Type == all[i].rec.Type && bytes.Equal(g.Payload, all[i].rec.Payload) {
				// Payloads could coincidentally collide only if two
				// distinct appended records are byte-identical; guard by
				// also requiring i < checkpointIdx are otherwise-uniquely
				// tagged (they are, since fileID/value differ per i).
				t.Errorf("record %d (before checkpoint index %d) unexpectedly appeared in the replayed sequence: %s %x", i, checkpointIdx, g.Type, g.Payload)
			}
		}
	}

	// "final state matches applying the same mutations directly": build the
	// state that would exist by applying every original mutation from
	// scratch, and compare it to (state-as-of-the-checkpoint) + (the
	// records Replay actually reapplied).
	direct := make(map[string]string)
	for _, a := range all {
		if err := applyToState(direct, a.rec); err != nil {
			t.Fatalf("applyToState(direct, %v): %v", a.rec, err)
		}
	}

	replayed := make(map[string]string)
	for i := 0; i < checkpointIdx; i++ {
		if err := applyToState(replayed, all[i].rec); err != nil {
			t.Fatalf("applyToState(replayed base, %v): %v", all[i].rec, err)
		}
	}
	for _, g := range got {
		if err := applyToState(replayed, g); err != nil {
			t.Fatalf("applyToState(replayed, %v): %v", g, err)
		}
	}

	if len(direct) != len(replayed) {
		t.Fatalf("final state mismatch: direct has %d keys, replayed has %d keys (direct=%v, replayed=%v)", len(direct), len(replayed), direct, replayed)
	}
	for k, v := range direct {
		if replayed[k] != v {
			t.Errorf("final state mismatch for key %q: direct=%q, replayed=%q", k, v, replayed[k])
		}
	}
}

// TestRecoveryReplayNoOpWhenCheckpointCoversEverything verifies that
// checkpointing at the exact end of the last segment makes Replay a correct
// no-op: apply must never be invoked.
func TestRecoveryReplayNoOpWhenCheckpointCoversEverything(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 128
	w, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}

	for i := 0; i < 20; i++ {
		rec := NewCatalogPutRecord(uint64(i), []byte(fmt.Sprintf("value-%03d", i)))
		if _, err := w.Append(rec.Encode()); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	lastSegNum := w.SegmentNum()
	if err := w.Close(); err != nil {
		t.Fatalf("Writer.Close: %v", err)
	}

	info, err := os.Stat(segmentPath(dir, lastSegNum))
	if err != nil {
		t.Fatalf("Stat last segment: %v", err)
	}

	if err := Checkpoint(dir, uint64(lastSegNum), info.Size()); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	callCount := 0
	err = Replay(dir, func(TypedRecord) error {
		callCount++
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: unexpected error: %v", err)
	}
	if callCount != 0 {
		t.Errorf("Replay invoked apply %d times, want 0 (checkpoint already covers everything)", callCount)
	}
}

// TestRecoveryReplayNoCheckpointStartsFromBeginning verifies that a WAL
// directory with no manifest.json at all (LoadCheckpoint's found=false case)
// replays every record from segment 0, offset 0 forward.
func TestRecoveryReplayNoCheckpointStartsFromBeginning(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 128
	w, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}

	var want []TypedRecord
	for i := 0; i < 25; i++ {
		rec := NewCatalogPutRecord(uint64(i), []byte(fmt.Sprintf("value-%03d", i)))
		if _, err := w.Append(rec.Encode()); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
		want = append(want, rec)
	}
	if w.SegmentNum() == 0 {
		t.Fatalf("test setup did not force any rotation; final SegmentNum=0")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Writer.Close: %v", err)
	}

	var got []TypedRecord
	err = Replay(dir, func(rec TypedRecord) error {
		got = append(got, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("Replay called apply %d times, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Type != want[i].Type || !bytes.Equal(got[i].Payload, want[i].Payload) {
			t.Errorf("replayed record %d = {%s, %x}, want {%s, %x}", i, got[i].Type, got[i].Payload, want[i].Type, want[i].Payload)
		}
	}
}

// TestRecoveryReplayInvalidRecordType closes the gap flagged in subtask
// 1.3.2's verification (regression.jsonl, run 038-verification):
// DecodeTypedRecord performs no validation of RecordType, so a corrupted or
// garbage record with an invalid/unrecognized type byte would previously
// decode without error. This test hand-crafts such a record (bypassing the
// NewXXXRecord helpers, which never produce an invalid type) and asserts
// Replay returns an error rather than silently skipping or succeeding.
func TestRecoveryReplayInvalidRecordType(t *testing.T) {
	dir := t.TempDir()

	const maxSegmentBytes = 4096
	w, err := OpenWriter(dir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}

	// A valid record first, to prove Replay gets far enough to reach the bad
	// one rather than failing for an unrelated reason.
	validRec := NewCatalogPutRecord(1, []byte("ok"))
	if _, err := w.Append(validRec.Encode()); err != nil {
		t.Fatalf("Append(valid): %v", err)
	}

	// Hand-craft a record whose type tag is RecordTypeInvalid (0), which
	// TypedRecord.Encode() would never itself produce.
	badRaw := TypedRecord{Type: RecordTypeInvalid, Payload: []byte("garbage")}.Encode()
	if _, err := w.Append(badRaw); err != nil {
		t.Fatalf("Append(invalid type): %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Writer.Close: %v", err)
	}

	callCount := 0
	err = Replay(dir, func(TypedRecord) error {
		callCount++
		return nil
	})
	if err == nil {
		t.Fatalf("Replay: got nil error, want a hard error for a record with an invalid/unrecognized RecordType")
	}
	if callCount != 1 {
		t.Errorf("Replay invoked apply %d times before erroring, want exactly 1 (only the valid record preceding the bad one)", callCount)
	}

	// Also verify an unrecognized-but-nonzero type byte (e.g. 99, never
	// assigned to any RecordType constant) is rejected the same way.
	dir2 := t.TempDir()
	w2, err := OpenWriter(dir2, maxSegmentBytes)
	if err != nil {
		t.Fatalf("OpenWriter (dir2): %v", err)
	}
	badRaw2 := TypedRecord{Type: RecordType(99), Payload: []byte("garbage")}.Encode()
	if _, err := w2.Append(badRaw2); err != nil {
		t.Fatalf("Append(dir2, unrecognized type): %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Writer.Close (dir2): %v", err)
	}

	err = Replay(dir2, func(TypedRecord) error { return nil })
	if err == nil {
		t.Fatalf("Replay (dir2): got nil error, want a hard error for an unrecognized RecordType byte (99)")
	}
}
