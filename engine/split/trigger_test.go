package split

import "testing"

// TestThresholdDetection covers task-2b.1.1's acceptance criteria: an append
// that pushes a file's size over the configured threshold triggers exactly
// one split-eligibility signal; appends that stay under threshold trigger
// none. Subtests map onto the four scenarios in the subtask brief plus the
// named edge cases (see .cdr/runs/2026-07-06/017-implementation/plan.md and
// validation-matrix.json for the full traceability mapping).
func TestThresholdDetection(t *testing.T) {
	const threshold = 100
	const fileID = uint64(42)

	trig, err := NewTrigger(threshold)
	if err != nil {
		t.Fatalf("NewTrigger(%d) returned unexpected error: %v", threshold, err)
	}

	t.Run("stays_under", func(t *testing.T) {
		sig, ok := trig.Detect(fileID, 10, 50)
		if ok {
			t.Fatalf("expected no signal for an append staying under threshold, got %+v", sig)
		}
	})

	t.Run("exact_boundary_no_signal", func(t *testing.T) {
		// Landing exactly ON the threshold must NOT count as crossing.
		sig, ok := trig.Detect(fileID, 90, threshold)
		if ok {
			t.Fatalf("expected no signal when append lands exactly on threshold, got %+v", sig)
		}
	})

	t.Run("crosses_by_one", func(t *testing.T) {
		sig, ok := trig.Detect(fileID, 99, 101)
		if !ok {
			t.Fatalf("expected a signal for an append crossing the threshold, got none")
		}
		want := Signal{FileID: fileID, OldSizeBytes: 99, NewSizeBytes: 101, ThresholdBytes: threshold}
		if sig != want {
			t.Fatalf("signal fields mismatch: got %+v, want %+v", sig, want)
		}
	})

	t.Run("already_over_no_resignal", func(t *testing.T) {
		// Simulates both "subsequent append when already over" and "file
		// starts already over threshold at hook install time": in both
		// cases oldSizeBytes is already > threshold before this specific
		// append, so it must not re-signal.
		sig, ok := trig.Detect(fileID, 150, 200)
		if ok {
			t.Fatalf("expected no re-signal when file already over threshold, got %+v", sig)
		}

		sig2, ok2 := trig.Detect(fileID, 500, 600)
		if ok2 {
			t.Fatalf("expected no retroactive signal for a file that starts already over threshold, got %+v", sig2)
		}
	})

	t.Run("cumulative_sequence_signals_once", func(t *testing.T) {
		// Multiple small appends that individually stay under threshold but
		// cumulatively cross it: the signal must fire on exactly the one
		// append that causes the crossing (90 -> 110), not before and not
		// again after.
		sizes := []uint64{0, 30, 60, 90, 110, 140}
		crossings := 0
		var crossingOld, crossingNew uint64

		for i := 1; i < len(sizes); i++ {
			old, new := sizes[i-1], sizes[i]
			sig, ok := trig.Detect(fileID, old, new)
			if ok {
				crossings++
				crossingOld, crossingNew = sig.OldSizeBytes, sig.NewSizeBytes
			}
		}

		if crossings != 1 {
			t.Fatalf("expected exactly 1 signal across the cumulative append sequence, got %d", crossings)
		}
		if crossingOld != 90 || crossingNew != 110 {
			t.Fatalf("expected the single signal to be for the 90->110 step, got %d->%d", crossingOld, crossingNew)
		}
	})

	t.Run("zero_byte_append_under", func(t *testing.T) {
		sig, ok := trig.Detect(fileID, 50, 50)
		if ok {
			t.Fatalf("expected no signal for a zero-byte append under threshold, got %+v", sig)
		}
	})

	t.Run("zero_byte_append_over", func(t *testing.T) {
		sig, ok := trig.Detect(fileID, 150, 150)
		if ok {
			t.Fatalf("expected no signal for a zero-byte append already over threshold, got %+v", sig)
		}
	})

	t.Run("shrinking_pair_no_signal", func(t *testing.T) {
		// Not a valid append-growth observation; defensively must not signal.
		sig, ok := trig.Detect(fileID, 200, 50)
		if ok {
			t.Fatalf("expected no signal for a shrinking (non-append) size pair, got %+v", sig)
		}
	})
}

// TestNewTriggerValidation covers threshold-of-zero (invalid configuration)
// being guarded against, and a minimal positive threshold being accepted.
func TestNewTriggerValidation(t *testing.T) {
	t.Run("zero_threshold_rejected", func(t *testing.T) {
		trig, err := NewTrigger(0)
		if err == nil {
			t.Fatalf("expected an error for a zero threshold, got nil (trigger=%+v)", trig)
		}
		if trig != nil {
			t.Fatalf("expected a nil Trigger alongside the error, got %+v", trig)
		}
	})

	t.Run("positive_threshold_accepted", func(t *testing.T) {
		trig, err := NewTrigger(1)
		if err != nil {
			t.Fatalf("NewTrigger(1) returned unexpected error: %v", err)
		}
		if trig.ThresholdBytes() != 1 {
			t.Fatalf("expected ThresholdBytes()==1, got %d", trig.ThresholdBytes())
		}
	})
}

// TestDefaultTrigger sanity-checks DefaultTrigger's configured threshold and
// that it correctly detects a crossing using the documented default.
func TestDefaultTrigger(t *testing.T) {
	trig := DefaultTrigger()

	if trig.ThresholdBytes() != DefaultThresholdBytes {
		t.Fatalf("expected DefaultTrigger's threshold to be DefaultThresholdBytes (%d), got %d",
			DefaultThresholdBytes, trig.ThresholdBytes())
	}

	sig, ok := trig.Detect(7, DefaultThresholdBytes-192, DefaultThresholdBytes+808)
	if !ok {
		t.Fatalf("expected DefaultTrigger to signal a crossing append, got none")
	}
	if sig.ThresholdBytes != DefaultThresholdBytes {
		t.Fatalf("expected signal's ThresholdBytes to be DefaultThresholdBytes (%d), got %d",
			DefaultThresholdBytes, sig.ThresholdBytes)
	}
}

// TestCrossesThreshold exercises the standalone pure predicate directly,
// independent of Trigger/Signal, matching Detect's documented semantics.
func TestCrossesThreshold(t *testing.T) {
	cases := []struct {
		name                string
		old, new, threshold uint64
		want                bool
	}{
		{"under", 10, 50, 100, false},
		{"exact_boundary", 90, 100, 100, false},
		{"crosses_by_one", 99, 101, 100, true},
		{"already_over", 150, 200, 100, false},
		{"zero_byte_under", 50, 50, 100, false},
		{"zero_byte_over", 150, 150, 100, false},
		{"shrinking_pair", 200, 50, 100, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CrossesThreshold(tc.old, tc.new, tc.threshold)
			if got != tc.want {
				t.Fatalf("CrossesThreshold(%d, %d, %d) = %v, want %v", tc.old, tc.new, tc.threshold, got, tc.want)
			}
		})
	}
}
