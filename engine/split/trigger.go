package split

import "fmt"

// DefaultThresholdBytes is the default split-eligibility size threshold: ~8KB,
// matching the ~2000-token default documented in docs/LLD/split.md's "Trigger"
// section (and the pre-existing, independent stub in
// engine/catalog/content.go's defaultSplitThresholdBytes — see trigger_test.go
// and docs/LLD/split.md for why the two are deliberately kept in sync in
// value, even though this package does not (yet) wire the two together).
const DefaultThresholdBytes = 8 * 1024

// Signal describes a single split-eligibility signal: the specific append
// that caused FileID's content size to cross the configured threshold.
//
// Signal is a pure value type carrying just enough context (which file, and
// the before/after/threshold sizes that triggered the signal) for a future
// consumer (e.g. the per-file CAS guard in a later subtask) to act on, and
// for tests/logs to assert on/observe without needing to separately thread
// that context through.
type Signal struct {
	// FileID identifies which file's append caused the crossing.
	FileID uint64

	// OldSizeBytes is the file's content size, in bytes, immediately before
	// the append that caused this signal.
	OldSizeBytes uint64

	// NewSizeBytes is the file's content size, in bytes, immediately after
	// the append that caused this signal. NewSizeBytes is always strictly
	// greater than ThresholdBytes for a Signal that was actually produced by
	// Trigger.Detect (see Detect's doc comment for the exact condition).
	NewSizeBytes uint64

	// ThresholdBytes is the configured threshold that was crossed, copied
	// from the Trigger that produced this Signal (so the Signal is fully
	// self-describing even if the Trigger's configuration changes later).
	ThresholdBytes uint64
}

// Trigger holds one configured, tunable size threshold and evaluates
// per-append before/after content sizes against it to detect
// split-eligibility crossings.
//
// Trigger is a size-threshold DETECTION hook only (subtask 2b.1.1): it never
// initiates, guards against duplicate, or executes an actual split. Those
// concerns belong to later, separate subtasks/files in this package (a
// per-file CAS guard and a catalog SPLITTING status transition — see
// docs/LLD/split.md's "Split sequence" and "Concurrency control" sections).
//
// Trigger is stateless and holds no per-file bookkeeping: it does not
// remember which fileIDs it has already signaled for. The
// exactly-one-signal-per-crossing property (see Detect's doc comment) is
// achieved structurally from the before/after sizes passed to each Detect
// call, not from memory inside Trigger. This is a deliberate design choice:
// introducing a second, independent "have I already signaled for this file"
// flag inside this package would risk drifting out of sync with the actual
// source of truth for a file's size (engine/catalog's CatalogRecord.SizeBytes),
// which is exactly the kind of subtle bug this epic's elevated correctness bar
// (see AGENT.md) is meant to guard against.
//
// Concurrency: Trigger itself needs no internal synchronization (it has no
// mutable state after construction), but it provides none either — callers
// evaluating concurrent appends to the SAME fileID are responsible for
// ensuring the (oldSizeBytes, newSizeBytes) pair passed to Detect for a given
// append reflects a serialized, non-torn view of that file's size
// transitions (today, engine/catalog/content.go's ContentStore.Append
// already provides exactly this via its own per-fileID striped mutex).
type Trigger struct {
	thresholdBytes uint64
}

// NewTrigger constructs a Trigger configured with thresholdBytes as its
// split-eligibility size threshold. thresholdBytes must be strictly
// positive; a zero threshold is rejected as invalid configuration (returned
// as an error, not a panic, matching this repo's error-return convention for
// invalid constructor arguments — see e.g. engine/catalog.OpenContentStore).
// Negative thresholds are not representable: thresholdBytes is a uint64, so
// only the zero case needs an explicit guard.
func NewTrigger(thresholdBytes uint64) (*Trigger, error) {
	if thresholdBytes == 0 {
		return nil, fmt.Errorf("split: NewTrigger: thresholdBytes must be positive, got %d", thresholdBytes)
	}
	return &Trigger{thresholdBytes: thresholdBytes}, nil
}

// DefaultTrigger constructs a Trigger using DefaultThresholdBytes. It never
// fails: DefaultThresholdBytes is a positive compile-time constant, so the
// only error NewTrigger can return is structurally unreachable here.
func DefaultTrigger() *Trigger {
	t, err := NewTrigger(DefaultThresholdBytes)
	if err != nil {
		// Unreachable: DefaultThresholdBytes is a positive constant.
		panic(fmt.Sprintf("split: DefaultTrigger: unreachable: %v", err))
	}
	return t
}

// ThresholdBytes returns t's configured split-eligibility size threshold.
func (t *Trigger) ThresholdBytes() uint64 {
	return t.thresholdBytes
}

// Detect evaluates a single append's before/after content sizes for fileID
// against t's configured threshold, returning (Signal, true) exactly when
// this specific append caused the crossing, and (Signal{}, false) otherwise.
//
// The crossing condition is: oldSizeBytes <= t.thresholdBytes AND
// newSizeBytes > t.thresholdBytes. In particular:
//   - Landing exactly ON the threshold (newSizeBytes == thresholdBytes) does
//     NOT count as crossing; the append must push the size STRICTLY over.
//   - A file already over threshold before this append
//     (oldSizeBytes > thresholdBytes) never re-signals, regardless of
//     newSizeBytes, so a caller invoking Detect once per append and only for
//     that append's own (before, after) pair gets exactly one signal for the
//     append that causes the crossing and none for any append before or
//     after it — including the very first append against a file that starts
//     out already over threshold (which must not retroactively signal).
//   - An append that does not grow the file's size (newSizeBytes <=
//     oldSizeBytes, including the zero-byte-append case newSizeBytes ==
//     oldSizeBytes) can never satisfy the crossing condition and so never
//     signals; Detect additionally treats any newSizeBytes < oldSizeBytes
//     pair defensively as "not an append" (a real append can only grow
//     size) and returns no signal, rather than guessing at intent.
//
// Detect performs no I/O and touches no shared state; it is safe to call
// from multiple goroutines concurrently for different or even the same
// fileID (though see Trigger's doc comment for the caller's responsibility
// around the (oldSizeBytes, newSizeBytes) values themselves being
// non-racing/non-torn for a given fileID).
func (t *Trigger) Detect(fileID, oldSizeBytes, newSizeBytes uint64) (Signal, bool) {
	if !CrossesThreshold(oldSizeBytes, newSizeBytes, t.thresholdBytes) {
		return Signal{}, false
	}
	return Signal{
		FileID:         fileID,
		OldSizeBytes:   oldSizeBytes,
		NewSizeBytes:   newSizeBytes,
		ThresholdBytes: t.thresholdBytes,
	}, true
}

// CrossesThreshold is the underlying pure boolean predicate behind
// Trigger.Detect, exported standalone so callers and tests can reason about
// (and assert on) the crossing condition directly, without constructing a
// Trigger or supplying a fileID. See Detect's doc comment for the exact
// semantics (strictly-over, no re-signal once already over, defensive
// shrinking-pair guard).
func CrossesThreshold(oldSizeBytes, newSizeBytes, thresholdBytes uint64) bool {
	if newSizeBytes < oldSizeBytes {
		// Not a valid append-growth observation; treat defensively as no signal.
		return false
	}
	return oldSizeBytes <= thresholdBytes && newSizeBytes > thresholdBytes
}
