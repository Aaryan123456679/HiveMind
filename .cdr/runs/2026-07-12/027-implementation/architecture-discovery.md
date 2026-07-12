# Architecture Discovery — Subtask 6.3.1

## Existing engine module layout (`engine/go.mod`)

Module `github.com/Aaryan123456679/HiveMind/engine`, go 1.26.4. `go.work` at repo root uses
`./api` and `./engine`. Existing top-level packages: `catalog`, `btree`, `graph`, `mvcc`,
`split`, `wal`, `rpc`, `cmd`, `loadtest`.

`engine/loadtest/` currently contains only a scaffold `doc.go`:
```go
// Package loadtest is part of the HiveMind storage engine.
package loadtest
```
No harness code, no tests, no dependency wiring yet — this run adds them.

## docs/HLD.md

Table entry (line 61): `loadtest/` = "Concurrency/load-generation harness for benchmarking",
pointing at `docs/LLD/eval.md`.

## docs/LLD/eval.md (authoritative design doc for this module)

States (status: scaffold only) that `engine/loadtest/` should be a "Custom load-generation
harness (goroutines + `sync.WaitGroup` + a results-collecting channel + histogram via
`hdrhistogram`)", used later for: ingestion throughput benchmarks, query-latency-under-load
(p50/p95/p99), and the auto-split race-correctness test. This matches the issue #32 wording
exactly and confirms hdrhistogram is the intended histogram library (not a hand-rolled one).

`docs/LLD/eval.md` also notes: "concurrency tests gated `go test -race` (see AGENT.md)" —
consistent with `AGENT.md`'s "All concurrency-sensitive tests must run under -race" rule.
This subtask's own unit test is not itself a concurrency-*correctness* race test (that's
6.3.4's job), but it does exercise concurrent goroutines, so it will run cleanly under
`-race` as good practice even though not strictly a race-correctness assertion.

## go.mod / go.sum: is hdrhistogram already a dependency?

Checked `engine/go.mod` — it only requires `google.golang.org/grpc` and
`google.golang.org/protobuf` (plus indirect x/net, x/sys, x/text, genproto). No hdrhistogram
anywhere in the repo (`go.sum`, `go.work.sum`, module cache) prior to this run.

Verified network/proxy access is available in this environment (`go get` successfully
resolved `github.com/HdrHistogram/hdrhistogram-go@latest` -> v1.3.0, the standard/most
widely used Go port of the HdrHistogram algorithm, API: `hdrhistogram.New(min, max int64,
sigfigs int) *Histogram`, `.RecordValue(v int64) error`, `.ValueAtQuantile(q float64) int64`,
`.TotalCount() int64`). This will be added as a direct dependency of `engine/go.mod`.

## Existing test-style conventions to follow

- Table-driven / plain `testing.T` tests, no external test framework (checked
  `engine/split/*_test.go`, `engine/wal/*_test.go`, `engine/catalog/*_test.go` — all stdlib
  `testing` only).
- Package-level doc comment convention: `// Package <name> ...` at top of `doc.go` (already
  present) — `harness.go` will carry the substantive package doc.
- Concurrency primitives already used elsewhere in the engine module: `sync.WaitGroup`,
  `sync.Mutex`/`RWMutex`, `sync/atomic` (seen in `engine/split/split_race_test.go`) — no new
  concurrency abstraction needed beyond stdlib + hdrhistogram.

## Conclusion / plan seed

Implement `engine/loadtest/harness.go` as a small, dependency-light, reusable package:
- A `Config`/`Run` (or `Harness`) type taking: goroutine count, total iteration count (or
  per-goroutine iteration count), and a caller-supplied `WorkFunc func(workerID, iter int)
  (latency time.Duration, err error)`.
- Spins up N goroutines via `sync.WaitGroup`, each calling the work function and sending a
  result struct (latency + error) on a buffered results channel.
- A separate aggregator goroutine (or the main goroutine after `wg.Wait()` + `close(ch)`)
  drains the channel into an `hdrhistogram.Histogram`, counting successes/errors.
- Exposes a `Result` struct with `TotalCount`, `ErrorCount`, `Duration` (wall-clock throughput
  window), and percentile accessors (`P50()`, `P95()`, `P99()`, or a generic
  `Percentile(q float64)`), sourced from the underlying `hdrhistogram.Histogram`.
- `harness_test.go` (`TestHarness...`) runs a trivial workload (fixed sleep/counter) through
  the harness and asserts the histogram's `TotalCount()` / result sample count matches the
  configured number of iterations, plus a basic sanity check on percentile ordering.

This design is intentionally generic (target-agnostic `WorkFunc`) so 6.3.2 (ingestion
throughput `testing.B`), 6.3.3 (query latency under load), and 6.3.4 (auto-split race at
scale) can all reuse the same `Harness`/`Run` entry point without modification — matching the
issue's explicit "reusable by later subtasks" requirement.
