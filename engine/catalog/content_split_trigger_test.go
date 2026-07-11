package catalog_test

// This file lives in the external catalog_test package (rather than alongside
// engine/catalog's other *_test.go files, which are all internal `package catalog`)
// specifically so it can import BOTH engine/catalog and engine/split in the same file
// without triggering a circular import: engine/split already imports engine/catalog (in
// execute.go and orchestrate.go), so an internal `package catalog` test file importing
// engine/split fails to build ("import cycle not allowed in test") — verified empirically
// during this subtask's architecture-discovery step. See
// engine/catalog/content.go's SplitTriggerFunc doc comment for the full rationale.

import (
	"path/filepath"
	"testing"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/split"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// newTestContentStore is this file's own (external-package) equivalent of
// engine/catalog's internal newTestContentStore test helper: wires up an isolated
// FileManager+Catalog, wal.Writer, and ContentStore under a fresh t.TempDir().
func newTestContentStore(t *testing.T) (cs *catalog.ContentStore, cat *catalog.Catalog) {
	t.Helper()

	root := t.TempDir()

	fm, err := catalog.Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		t.Fatalf("catalog.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := fm.Close(); err != nil {
			t.Errorf("FileManager.Close: %v", err)
		}
	})
	cat = catalog.NewCatalog(fm)

	w, err := wal.OpenWriter(filepath.Join(root, "wal"), 1<<20)
	if err != nil {
		t.Fatalf("wal.OpenWriter: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("wal.Writer.Close: %v", err)
		}
	})

	cs, err = catalog.OpenContentStore(root, cat, w)
	if err != nil {
		t.Fatalf("catalog.OpenContentStore: %v", err)
	}

	return cs, cat
}

// TestAppendTriggersSplitSignal covers subtask 4.5.3.1 (issue #40)'s exact test spec:
// ContentStore.Append, wired via SetSplitTrigger to a real *split.Trigger (rather than
// ContentStore's own fallback local comparison), fires the split-eligibility signal
// exactly once — on the one append whose resulting size first pushes the file strictly
// over the configured threshold — and never before or after, proving the production
// wiring genuinely delegates to engine/split/trigger.go's Trigger/Signal/CrossesThreshold
// detection logic rather than relying only on engine/split/trigger_test.go's own,
// separate unit coverage of that logic in isolation.
func TestAppendTriggersSplitSignal(t *testing.T) {
	cs, cat := newTestContentStore(t)

	const threshold = 64
	trig, err := split.NewTrigger(threshold)
	if err != nil {
		t.Fatalf("split.NewTrigger: %v", err)
	}

	var (
		detectCalls  int
		signals      []split.Signal
		gotFileID    uint64
		gotOld       uint64
		gotNew       uint64
		lastSawInput bool
	)
	cs.SetSplitTrigger(func(fileID, oldSizeBytes, newSizeBytes uint64) bool {
		detectCalls++
		gotFileID, gotOld, gotNew = fileID, oldSizeBytes, newSizeBytes
		lastSawInput = true
		sig, crossed := trig.Detect(fileID, oldSizeBytes, newSizeBytes)
		if crossed {
			signals = append(signals, sig)
		}
		return crossed
	})

	const fileID = uint64(7)
	initial := []byte("start")
	rec := catalog.CatalogRecord{
		FileID:         fileID,
		PathHash:       fileID * 31,
		CurrentVersion: 1,
		SizeBytes:      uint64(len(initial)),
		Status:         catalog.StatusActive,
	}
	if _, err := cs.Create(rec, initial); err != nil {
		t.Fatalf("Create: %v", err)
	}

	chunk := []byte("0123456789") // 10 bytes per append
	cumulative := append([]byte(nil), initial...)

	const numAppends = 10
	for i := 0; i < numAppends; i++ {
		crossed, err := cs.Append(fileID, chunk)
		if err != nil {
			t.Fatalf("Append(#%d): %v", i, err)
		}
		cumulative = append(cumulative, chunk...)

		wantCrossed := split.CrossesThreshold(uint64(len(cumulative)-len(chunk)), uint64(len(cumulative)), threshold)
		if crossed != wantCrossed {
			t.Fatalf("Append(#%d): crossed = %v, want %v (cumulative size %d, threshold %d)", i, crossed, wantCrossed, len(cumulative), threshold)
		}
	}

	if !lastSawInput {
		t.Fatalf("cs.splitTrigger hook was never invoked")
	}
	if detectCalls != numAppends {
		t.Fatalf("split trigger hook invoked %d times, want exactly %d (once per append, per acceptance criteria's 'on every append')", detectCalls, numAppends)
	}
	if len(signals) != 1 {
		t.Fatalf("split trigger fired %d times, want exactly 1 (at the crossing point); signals=%+v", len(signals), signals)
	}

	got := signals[0]
	if got.FileID != fileID {
		t.Fatalf("Signal.FileID = %d, want %d", got.FileID, fileID)
	}
	if got.ThresholdBytes != threshold {
		t.Fatalf("Signal.ThresholdBytes = %d, want %d", got.ThresholdBytes, threshold)
	}
	if got.OldSizeBytes > threshold {
		// Sanity guard only: OldSizeBytes must not itself already be over threshold
		// (that would mean this fired on a later, non-crossing append).
		t.Fatalf("Signal.OldSizeBytes = %d unexpectedly already over threshold %d", got.OldSizeBytes, threshold)
	}
	if got.NewSizeBytes <= threshold {
		t.Fatalf("Signal.NewSizeBytes = %d, want strictly greater than threshold %d", got.NewSizeBytes, threshold)
	}

	// Cross-check the last hook invocation's raw inputs and the catalog's SizeBytes
	// stay in lockstep with the content actually appended.
	gotRec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("cat.Get: %v", err)
	}
	if gotRec.SizeBytes != uint64(len(cumulative)) {
		t.Fatalf("final SizeBytes = %d, want %d", gotRec.SizeBytes, len(cumulative))
	}
	if gotNew != uint64(len(cumulative)) {
		t.Fatalf("last hook call's newSizeBytes = %d, want %d", gotNew, len(cumulative))
	}
	if gotOld >= gotNew {
		t.Fatalf("last hook call's oldSizeBytes (%d) not less than newSizeBytes (%d)", gotOld, gotNew)
	}
	if gotFileID != fileID {
		t.Fatalf("last hook call's fileID = %d, want %d", gotFileID, fileID)
	}
}
