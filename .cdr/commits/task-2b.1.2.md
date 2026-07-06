# task-2b.1.2 — Per-file CAS `splitInProgress` guard (subtask 2 of 3, issue #10)

## Summary
Second of three subtasks under GitHub issue #10 ("[2b] Split trigger + per-file CAS guard", Epic Phase 2b: Auto-split). Adds `engine/split/guard.go`: a per-file `FileGuard` providing a real `atomic.CompareAndSwap`-backed `splitInProgress` flag, guaranteeing that when many goroutines concurrently observe the same file crossing its split threshold, exactly one wins the CAS and initiates a split while all others back off without duplicating work.

## Features
- `engine/split.FileGuard`: per-fileID atomic CAS guard for split-in-progress state, keyed by `fileID` (the natural key shared with `Signal.FileID` from 2b.1.1's `Trigger`).
- Real CAS primitive (`atomic.CompareAndSwap` equivalent) — not a plain mutex+bool, closing off the TOCTOU class of bug explicitly called out in the issue's acceptance criteria.
- Lazy per-fileID entry creation guarded by a mutex that only protects map insertion, not steady-state CAS operations — matching `engine/btree/latch.go`'s `NodeStore` precedent for avoiding cross-key contention.
- `TestSplitInProgressCAS`: many goroutines race the CAS flag on the same fileID; asserts via an atomic counter that exactly one split-initiation call wins.

## Impact
- This closes out **subtask 2 of 3** under issue #10. **2b.1.3** (catalog `SPLITTING` status transition, `engine/split/orchestrate.go`) is the last remaining subtask before issue #10 as a whole can close.
- Verification explicitly traced the CAS correctness surface end-to-end (per `engine/split/` being flagged the system's highest-risk correctness area): confirmed no TOCTOU bug from a disguised mutex+bool, and no double-map-entry / lost-update bug in the lazy per-fileID map-creation path. Confidence: high.
- Scope discipline held: this subtask is the CAS guard primitive only — it does not perform the actual split and is not wired into `engine/catalog` (that remains 2b.1.3's job). Nothing was touched in `engine/catalog/` or `engine/btree/`.
- `FileGuard`'s per-fileID registry has the same deliberate no-eviction growth characteristic as `engine/btree/latch.go`'s `NodeStore` (task-2a.4.1, issue #9 follow-up): entries accumulate for every distinct fileID ever guarded and are never evicted. This is tracked as a non-blocking carried-forward item in `.cdr/memory/pending.md` (line 9), to be revisited alongside the matching `NodeStore` item rather than fixed ad hoc in this subtask.
- No regression: build/vet/gofmt clean engine-wide; full `go test ./split/...` green, including a 100x-repeated `-race` run of `TestSplitInProgressCAS` to stress the concurrency guarantee.

## Verification
- **Verdict**: PASS
- **Run ID**: 2026-07-07-005-verification
- **Details**: All 9 dimensions passed cleanly (requirements, architecture conformance, concurrency correctness, regression risk, edge cases, test coverage, security, performance, maintainability, build verification — no comments). Zero findings. Confidence: high. Verifier confirmed no plain mutex+bool TOCTOU bug and no double-checked-locking-without-a-lock bug in the lazy map-creation path. Build/test evidence: `go build ./...` OK, `go vet ./...` OK, `gofmt -l .` clean, `go test ./split/... -race -run TestSplitInProgressCAS -count=100 -timeout 10m` all pass, `go test ./split/... -race -v -count=1 -timeout 10m` all pass. Commit reviewed: `bed7edd` (feat).

## Release Notes
`engine/split` gains its CAS-backed `FileGuard`, ensuring exactly one goroutine ever initiates a split for a given file even when many observe the same threshold crossing concurrently — closing the TOCTOU risk class explicitly called out for this highest-risk correctness surface. This is subtask 2 of 3 toward full auto-split support (issue #10); the catalog `SPLITTING`-status wiring (2b.1.3) is the last piece before the guard is connected to the live split path. No breaking API change; new package surface only.
