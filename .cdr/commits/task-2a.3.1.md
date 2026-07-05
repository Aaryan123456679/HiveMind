# task-2a.3.1 — Concurrent stress test across many fileIDs verifying stripe isolation

## Summary
Closes out subtask 1 of 2 under task-2a.3 (Catalog concurrent-correctness hardening, GitHub issue #8). Adds `TestStripedConcurrencyStress` (`engine/catalog/catalog_test.go`), a broad concurrency stress test that drives 2000 distinct fileIDs (roughly 7.8x the 256-stripe count) through concurrent Put/Delete workloads, one dedicated goroutine per fileID, and checks each fileID's final state against a statically-derived serial-execution oracle. The test targets the specific risk that Catalog's 256 striped mutexes could let cross-fileID interference corrupt state under real concurrent load, either via lock-ordering bugs or incorrect stripe hashing.

## Features
- `TestStripedConcurrencyStress`: 2000 fileIDs, one goroutine each, executing one of 5 fixed deterministic Put/Delete patterns (keyed by `fileID % 5`).
- Oracle: expected final state (present-with-version or absent) derived statically per pattern from its own op sequence, not from runtime observation — a genuine serial-execution reference.
- Stripe collision coverage: fileID count chosen so multiple fileIDs collide per stripe on average, exercising cross-fileID isolation under real lock contention rather than trivially disjoint stripes.
- Run under `-race`; stable across repeated runs (`-count=10`).

## Impact
Test-only change; no production code touched. Strengthens confidence in the striped-lock design introduced for Catalog by proving, under genuine concurrent load with meaningful stripe collisions, that unrelated fileIDs cannot corrupt each other's state. This is subtask 1 of 2 for task-2a.3; task-2a.3 and its parent GitHub issue #8 remain open pending subtask 2a.3.2.

## Verification
- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `2026-07-05-011-verification`
- Verification hand-traced all 5 stress patterns' expected final states, confirmed genuine concurrency (not just goroutine-scheduled sequential execution), confirmed the 2000-fileID/256-stripe ratio produces meaningful stripe collisions, confirmed the test would catch real cross-fileID corruption bugs if introduced, zero regressions in the full package, and clean results at `-count=10`.
- Non-blocking comment: no single pattern exercises delete-of-nonexistent or get-of-never-created *within* the concurrent workload itself; flagged as a future nice-to-have, not a correctness gap in what the test does cover.

## Release Notes
Added a concurrent stress test validating that Catalog's striped-lock design correctly isolates unrelated fileIDs under real concurrent Put/Delete load across 2000 fileIDs. Test-only change — no production behavior changes.
