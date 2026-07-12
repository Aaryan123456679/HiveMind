// Package loadtest is part of the HiveMind storage engine.
//
// It provides a small, target-agnostic load-generation harness: spin up a
// configurable number of goroutines that each execute a caller-supplied
// [WorkFunc], collect the resulting latency samples over a channel, and
// aggregate them into an HdrHistogram-backed [Result] exposing throughput
// and latency percentiles (p50/p95/p99, or any arbitrary quantile).
//
// This package is intentionally generic so it can be reused, unmodified, by
// later load tests against different subsystems:
//   - concurrent ingestion throughput benchmarks (LLM/segmentation calls
//     mocked out, isolating the storage engine itself)
//   - concurrent query latency under concurrent ingestion load
//   - auto-split race-correctness at scale (many goroutines appending to the
//     same file simultaneously)
//
// See docs/LLD/eval.md for the design rationale.
package loadtest

import (
	"errors"
	"fmt"
	"sync"
	"time"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

// WorkFunc is a single unit of work executed by a harness worker goroutine.
// workerID is the zero-based index of the worker goroutine running this
// call; iter is the zero-based index of this call within that worker's
// sequence of iterations. WorkFunc returns the latency to record for this
// call and an optional error; a non-nil error marks the sample as failed
// (it is still counted, but excluded from the latency histogram).
type WorkFunc func(workerID, iter int) (latency time.Duration, err error)

// Config configures a harness [Run]. Zero-value fields are replaced with
// sane defaults by [Run] (see DefaultConfig), so callers may populate only
// the fields they care about.
type Config struct {
	// Workers is the number of concurrent goroutines to spin up. Defaults
	// to 1 if <= 0.
	Workers int

	// Iterations is the number of times each worker invokes the WorkFunc.
	// Defaults to 1 if <= 0. Total sample count produced by a run is
	// Workers * Iterations.
	Iterations int

	// MinLatency and MaxLatency bound the values the underlying
	// hdrhistogram.Histogram can record. Defaults to 1 microsecond and 10
	// minutes respectively, which comfortably covers realistic in-process
	// storage-engine and RPC latencies.
	MinLatency time.Duration
	MaxLatency time.Duration

	// SigFigs is the number of significant decimal digits of precision the
	// histogram retains (hdrhistogram accepts 1-5). Defaults to 3.
	SigFigs int
}

// DefaultConfig returns a Config with all fields set to their default
// values (1 worker, 1 iteration, 1us-10min range, 3 significant figures).
func DefaultConfig() Config {
	return Config{
		Workers:    1,
		Iterations: 1,
		MinLatency: time.Microsecond,
		MaxLatency: 10 * time.Minute,
		SigFigs:    3,
	}
}

// withDefaults returns a copy of cfg with any zero-value fields replaced by
// DefaultConfig's values.
func (cfg Config) withDefaults() Config {
	def := DefaultConfig()
	if cfg.Workers <= 0 {
		cfg.Workers = def.Workers
	}
	if cfg.Iterations <= 0 {
		cfg.Iterations = def.Iterations
	}
	if cfg.MinLatency <= 0 {
		cfg.MinLatency = def.MinLatency
	}
	if cfg.MaxLatency <= 0 {
		cfg.MaxLatency = def.MaxLatency
	}
	if cfg.SigFigs <= 0 {
		cfg.SigFigs = def.SigFigs
	}
	return cfg
}

// sample is a single result sent over the harness's internal results
// channel by a worker goroutine.
type sample struct {
	latency time.Duration
	err     error
}

// Result is the aggregated outcome of a harness [Run]: sample counts and a
// latency histogram (successful samples only), plus wall-clock elapsed time
// for throughput reporting.
type Result struct {
	// TotalCount is the total number of WorkFunc invocations (successes +
	// errors).
	TotalCount int

	// SuccessCount is the number of WorkFunc invocations that returned a
	// nil error; these are the samples recorded into the histogram.
	SuccessCount int

	// ErrorCount is the number of WorkFunc invocations that returned a
	// non-nil error. These are counted but excluded from the latency
	// histogram, since a failed call's "latency" is not a meaningful
	// latency sample.
	ErrorCount int

	// Elapsed is the wall-clock duration of the run, from just before the
	// first worker goroutine starts to just after the last one finishes.
	Elapsed time.Duration

	hist *hdrhistogram.Histogram
}

// Percentile returns the latency value at the given quantile (0-100) of the
// successful-sample histogram. Returns 0 if there are no successful
// samples.
func (r *Result) Percentile(q float64) time.Duration {
	if r.hist == nil || r.SuccessCount == 0 {
		return 0
	}
	return time.Duration(r.hist.ValueAtQuantile(q))
}

// P50 returns the median latency of successful samples.
func (r *Result) P50() time.Duration { return r.Percentile(50) }

// P95 returns the 95th-percentile latency of successful samples.
func (r *Result) P95() time.Duration { return r.Percentile(95) }

// P99 returns the 99th-percentile latency of successful samples.
func (r *Result) P99() time.Duration { return r.Percentile(99) }

// Throughput returns the number of completed calls (successes + errors) per
// second, based on Elapsed. Returns 0 if Elapsed is 0.
func (r *Result) Throughput() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.TotalCount) / r.Elapsed.Seconds()
}

// Histogram returns the underlying hdrhistogram.Histogram of successful
// samples' latencies, for callers that need direct access (e.g. exporting
// percentile distributions, merging across runs).
func (r *Result) Histogram() *hdrhistogram.Histogram { return r.hist }

// Run spins up cfg.Workers goroutines, each invoking work cfg.Iterations
// times, collects every invocation's (latency, error) via an internal
// results channel, and aggregates the successful samples' latencies into an
// hdrhistogram-backed Result. Run blocks until all workers have finished.
//
// Run returns an error only if cfg is invalid after defaulting (it never
// is, today, since invalid fields are silently defaulted) or if work is
// nil.
func Run(cfg Config, work WorkFunc) (*Result, error) {
	if work == nil {
		return nil, errors.New("loadtest: Run: work must not be nil")
	}
	cfg = cfg.withDefaults()

	hist := hdrhistogram.New(cfg.MinLatency.Nanoseconds(), cfg.MaxLatency.Nanoseconds(), cfg.SigFigs)

	total := cfg.Workers * cfg.Iterations
	results := make(chan sample, total)

	start := time.Now()

	var wg sync.WaitGroup
	wg.Add(cfg.Workers)
	for w := 0; w < cfg.Workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < cfg.Iterations; i++ {
				latency, err := work(workerID, i)
				results <- sample{latency: latency, err: err}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(results)

	res := &Result{hist: hist, Elapsed: elapsed}
	for s := range results {
		res.TotalCount++
		if s.err != nil {
			res.ErrorCount++
			continue
		}
		res.SuccessCount++
		if recErr := hist.RecordValue(s.latency.Nanoseconds()); recErr != nil {
			// A value outside [MinLatency, MaxLatency]. Clamp into range
			// rather than dropping the sample silently, so TotalCount /
			// SuccessCount always account for every WorkFunc invocation.
			clamped := s.latency.Nanoseconds()
			if clamped < cfg.MinLatency.Nanoseconds() {
				clamped = cfg.MinLatency.Nanoseconds()
			} else if clamped > cfg.MaxLatency.Nanoseconds() {
				clamped = cfg.MaxLatency.Nanoseconds()
			}
			if err2 := hist.RecordValue(clamped); err2 != nil {
				return nil, fmt.Errorf("loadtest: Run: failed to record clamped latency sample: %w", err2)
			}
		}
	}

	return res, nil
}
