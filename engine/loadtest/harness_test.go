package loadtest

import (
	"errors"
	"testing"
	"time"
)

// TestHarnessAggregatesConcurrentSamples is subtask 6.3.1's test-spec test:
// "run a trivial workload through the harness, assert histogram output has
// the expected sample count." (go test ./engine/loadtest/... -run TestHarness)
func TestHarnessAggregatesConcurrentSamples(t *testing.T) {
	const workers = 8
	const iterations = 100
	const wantTotal = workers * iterations

	cfg := Config{Workers: workers, Iterations: iterations}

	work := func(workerID, iter int) (time.Duration, error) {
		// Deterministic, cheap, trivial workload: latency proportional to
		// iter so the histogram sees a real spread of values rather than a
		// single constant (which would make percentile-ordering assertions
		// vacuous).
		return time.Duration(iter+1) * time.Microsecond, nil
	}

	res, err := Run(cfg, work)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	if res.TotalCount != wantTotal {
		t.Errorf("TotalCount = %d, want %d", res.TotalCount, wantTotal)
	}
	if res.SuccessCount != wantTotal {
		t.Errorf("SuccessCount = %d, want %d", res.SuccessCount, wantTotal)
	}
	if res.ErrorCount != 0 {
		t.Errorf("ErrorCount = %d, want 0", res.ErrorCount)
	}
	if got := res.Histogram().TotalCount(); got != int64(wantTotal) {
		t.Errorf("Histogram().TotalCount() = %d, want %d", got, wantTotal)
	}

	p50, p95, p99 := res.P50(), res.P95(), res.P99()
	if p50 <= 0 {
		t.Errorf("P50 = %v, want > 0", p50)
	}
	if p50 > p95 || p95 > p99 {
		t.Errorf("percentiles not monotonically non-decreasing: p50=%v p95=%v p99=%v", p50, p95, p99)
	}

	if res.Elapsed <= 0 {
		t.Errorf("Elapsed = %v, want > 0", res.Elapsed)
	}
	if tp := res.Throughput(); tp <= 0 {
		t.Errorf("Throughput() = %v, want > 0", tp)
	}
}

// TestHarnessRecordsErrors confirms failed WorkFunc invocations are counted
// (SuccessCount/ErrorCount split) but excluded from the latency histogram,
// so no-data-loss-style assertions built on top of this harness (e.g.
// 6.3.4's auto-split race test) can rely on TotalCount always accounting
// for every invocation regardless of success/failure.
func TestHarnessRecordsErrors(t *testing.T) {
	const workers = 4
	const iterations = 50
	const wantTotal = workers * iterations

	errBoom := errors.New("boom")

	work := func(workerID, iter int) (time.Duration, error) {
		if iter%5 == 0 {
			return 0, errBoom
		}
		return time.Millisecond, nil
	}

	res, err := Run(Config{Workers: workers, Iterations: iterations}, work)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	wantErrors := workers * 10 // iter in [0,50), iter%5==0 -> 10 per worker
	wantSuccess := wantTotal - wantErrors

	if res.TotalCount != wantTotal {
		t.Errorf("TotalCount = %d, want %d", res.TotalCount, wantTotal)
	}
	if res.ErrorCount != wantErrors {
		t.Errorf("ErrorCount = %d, want %d", res.ErrorCount, wantErrors)
	}
	if res.SuccessCount != wantSuccess {
		t.Errorf("SuccessCount = %d, want %d", res.SuccessCount, wantSuccess)
	}
	if got := res.Histogram().TotalCount(); got != int64(wantSuccess) {
		t.Errorf("Histogram().TotalCount() = %d, want %d (errors must be excluded)", got, wantSuccess)
	}
}

// TestHarnessConfigDefaults confirms a zero-value Config still produces a
// working single-worker/single-iteration run via Config.withDefaults.
func TestHarnessConfigDefaults(t *testing.T) {
	called := false
	work := func(workerID, iter int) (time.Duration, error) {
		called = true
		return time.Microsecond, nil
	}

	res, err := Run(Config{}, work)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if !called {
		t.Fatal("work function was never invoked")
	}
	if res.TotalCount != 1 {
		t.Errorf("TotalCount = %d, want 1", res.TotalCount)
	}
	if res.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d, want 1", res.SuccessCount)
	}
}

// TestHarnessNilWorkFunc confirms Run rejects a nil WorkFunc rather than
// panicking.
func TestHarnessNilWorkFunc(t *testing.T) {
	if _, err := Run(DefaultConfig(), nil); err == nil {
		t.Fatal("expected error for nil WorkFunc, got nil")
	}
}
