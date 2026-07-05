# task-2a.3.2 — Contention benchmark: striped mutex vs global-lock baseline

## Summary

Closes out subtask 2 of 2 under task-2a.3 (Catalog concurrent-correctness hardening,
GitHub issue #8). Adds `BenchmarkStripedVsGlobalLock`
(`engine/catalog/catalog_bench_test.go`), a contention benchmark that
demonstrates Catalog's 256-way striped-mutex design sustains materially higher
concurrent throughput than a naive single-global-lock design under many-fileID
contention. This is the last subtask of task-2a.3; with both 2a.3.1 (verified
stress test) and 2a.3.2 (verified benchmark) complete, task-2a.3 and its parent
GitHub issue #8 are closable.

## Features

- `BenchmarkStripedVsGlobalLock`: "Striped" and "GlobalLock" sub-benchmarks
  driving an identical `b.RunParallel` Put+Get workload across 4096 fileIDs
  against a minimal in-memory harness that reuses Catalog's real
  `stripeFor(fileID)` hash and `CatalogRecord.Encode`/`Decode`, differing only
  in lock granularity (256 per-stripe mutexes vs. one shared mutex).
- Deliberate harness pivot: an earlier iteration wrapped the real
  disk-backed Catalog/FileManager for both variants, but per-op pread/pwrite
  syscall cost dominated and masked the sub-100ns lock-contention signal.
  The in-memory harness isolates the locking-strategy variable as the sole
  difference between variants, giving a fair apples-to-apples comparison.
- Measured result: Striped ~230 ns/op vs GlobalLock ~454 ns/op (~2x
  throughput advantage for striped), reproduced consistently across repeated
  runs and independently re-confirmed 3x during verification.

## Impact

Test-only change; no production code touched. Provides durable, reproducible
evidence that Catalog's striped-mutex design meaningfully outperforms a
global-lock baseline under concurrent load, closing the loop opened by
2a.3.1's correctness stress test with a performance justification for the
striping strategy. Last subtask of task-2a.3 (issue #8) — parent task and
issue are now closable.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `2026-07-05-014-verification`
- Independently re-ran the benchmark 3x, confirmed a reproducible ~2x
  throughput advantage for striped vs. global-lock (~230ns vs ~455ns per op).
- Confirmed the in-memory harness is a fair, apples-to-apples isolation of
  the locking-strategy variable: it reuses real production
  `stripeFor`/`Encode`/`Decode`, and both variants perform identical work
  aside from lock granularity.
- Confirmed test-only change, zero regressions.

## Release Notes

- Added a benchmark (`BenchmarkStripedVsGlobalLock`) demonstrating and
  quantifying the throughput advantage of Catalog's striped-mutex design
  (~2x vs. a single global lock) under concurrent fileID contention.
  Test-only; no user-facing or production behavior change.
