# Requirement — GitHub issue #8, subtask 2a.3.2 (verbatim)

Subtask 2a.3.2 — Contention benchmark: striped mutex vs. single global-lock baseline

Acceptance criteria: A benchmark demonstrates the striped-mutex implementation
sustains meaningfully higher concurrent throughput than a naive single-global-lock
baseline under many-fileID contention.

Test spec: `go test ./engine/catalog/... -bench BenchmarkStripedVsGlobalLock -benchmem`:
report throughput numbers for both implementations side by side.

Impacted modules: `engine/catalog/catalog_bench_test.go` (new file).

This is the LAST subtask under task-2a.3 (issue #8). Subtask 2a.3.1 (concurrent
stress test verifying stripe isolation) is already implemented and verified
(commit 15f5dd5).
