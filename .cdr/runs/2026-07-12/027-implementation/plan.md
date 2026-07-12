# Plan — Subtask 6.3.1

1. Add `github.com/HdrHistogram/hdrhistogram-go` as a direct dependency of `engine/go.mod`
   (`go get`, then `go mod tidy` for that module).
2. Write `engine/loadtest/harness.go`:
   - `type WorkFunc func(workerID, iter int) (time.Duration, error)`
   - `type Config struct { Workers int; Iterations int; MinLatency, MaxLatency time.Duration; SigFigs int }`
     with a `DefaultConfig()`-style helper / sane zero-value fallbacks so callers can pass a
     partially-populated `Config`.
   - `type sample struct { latency time.Duration; err error }`
   - `type Result struct` wrapping `*hdrhistogram.Histogram`, `TotalCount`, `SuccessCount`,
     `ErrorCount`, `Elapsed time.Duration`, plus `Percentile(q float64) time.Duration`,
     `P50/P95/P99()` convenience wrappers, `Throughput() float64` (ops/sec over `Elapsed`).
   - `func Run(cfg Config, work WorkFunc) (*Result, error)`:
     - validates cfg (workers >=1, iterations >=1), fills defaults.
     - creates buffered `chan sample` sized `cfg.Workers*cfg.Iterations` (or a smaller bound
       with a draining goroutine — buffered to total count is simplest and avoids a separate
       drain-goroutine correctness concern for this harness's own scope).
     - `sync.WaitGroup` with `cfg.Workers` goroutines, each running `cfg.Iterations` calls to
       `work`, sending a `sample` per call.
     - after `wg.Wait()`, close channel, drain into `hdrhistogram.New(...)`, tally
       success/error counts, record `Elapsed`.
     - returns `*Result`.
3. Write `engine/loadtest/harness_test.go`:
   - `TestHarnessAggregatesConcurrentSamples` (matches `-run TestHarness` per issue's test
     spec): trivial workload (e.g. deterministic sleep proportional to iter, or fixed
     constant latency + a counter) run through `Run` with e.g. 8 workers x 100 iterations;
     assert `Result.TotalCount == 800`, `SuccessCount == 800`, `ErrorCount == 0`, and
     percentile ordering `P50 <= P95 <= P99`.
   - `TestHarnessRecordsErrors`: workload that errors on some iterations; assert
     `ErrorCount`/`SuccessCount` split is correct and errored samples are excluded from the
     histogram's total count (or included per design — decide explicitly in code + assert
     that decision).
   - `TestHarnessConfigDefaults` (optional, small): zero-value Config still produces a
     working single-worker/iteration run — cheap extra coverage of the defaulting logic.
4. Run `go build ./...` and `go test ./engine/loadtest/... -race` from repo root; iterate on
   any failures.
5. Write validation-matrix.json mapping each acceptance-criterion/test-spec line to the
   concrete test(s) above.
6. self-consistency check (build green + matrix fully covered) — internal sanity only, not
   verification.
7. One local commit, Problem/Solution/Impact style.
8. handoff.json with pointers only.
