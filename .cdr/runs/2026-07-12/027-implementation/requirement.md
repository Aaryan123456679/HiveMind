# Requirement — Subtask 6.3.1 (GitHub issue #32, "Go load tests")

Source: `gh issue view 32` (Phase 6: Demo + deployment + load tests epic, impacted module
`engine/loadtest/`).

## 6.3.1 — Load-generation harness (goroutines + sync.WaitGroup + results channel + hdrhistogram)

- **Acceptance criteria**: reusable harness spins up a configurable number of goroutines,
  collects results via a channel, produces latency histograms via hdrhistogram.
- **Test spec**: `go test ./engine/loadtest/... -run TestHarness`: run a trivial workload
  through the harness, assert histogram output has the expected sample count.
- **Impacted modules**: `engine/loadtest/harness.go`, `engine/loadtest/harness_test.go`.

## Downstream subtasks that must be able to reuse this harness (context only, not in scope here)

- 6.3.2 — Concurrent ingestion throughput benchmark (`testing.B`, LLM/segmentation calls
  mocked out) — `engine/loadtest/ingestion_bench_test.go`.
- 6.3.3 — Concurrent query latency under concurrent ingestion load (p50/p95/p99 flat,
  MVCC no-reader-blocking) — `engine/loadtest/query_latency_test.go`.
- 6.3.4 — Auto-split race-correctness at scale (`-race`, `TestAutoSplitRaceAtScale`,
  no-data-loss / exactly-one-split / no-dangling-edges) — `engine/loadtest/split_race_scale_test.go`.

## Scope of this run

Implement ONLY 6.3.1: the reusable harness package itself, plus its own unit test(s)
confirming correct concurrent-sample aggregation. Do NOT implement 6.3.2/6.3.3/6.3.4
benchmarks — those are separate subtasks that will consume this harness later.
