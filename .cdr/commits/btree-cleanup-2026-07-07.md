# btree-cleanup-2026-07-07 — pending.md cleanup (3 items, post-Phase-2a)

## Summary
Ad-hoc technical-debt cleanup (not tied to a GitHub issue/subtask) closing out 3 of the 5 outstanding items tracked in `.cdr/memory/pending.md` following Epic Phase 2a's closure. Addresses two stale/overclaiming doc comments left behind by task-2a.4.1's concurrency-control redesign, plus adds new observability into the btree's restart-from-root retry paths.

## Features
- `engine/btree/node.go`: reworded `LeafNode.Version`/`InternalNode.Version` doc comments to plainly state these are on-disk fields not used for in-process concurrency control, removing stale references to future CAS/atomic-bump work superseded by `latch.go`'s in-memory `nodeLatch.version` (landed in task-2a.4.1).
- `engine/btree/lookup.go`: reworded `Tree.Lookup`'s doc comment to scope its lock-free guarantee to per-node latches, explicitly documenting the narrow, intentional `rootMu` acquisition via `t.Root()` as an exception rather than leaving the prior "never calls Lock/TryLock anywhere" overclaim in place. Doc-only; no logic change.
- New additive `atomic.Uint64` restart counter + exported `RestartFromRootCount()` accessor in `latch.go`, incremented once per restart-from-root iteration in `crabInsert`, `crabDelete`, and `Tree.Lookup`'s retry loop. No behavioral change to retry/backoff logic (no cap added, by design). New test forces real restart-from-root contention and asserts the counter advances.

## Impact
- Closes 3 of 5 `pending.md` items; 2 remain open and unmodified (btree `SaveRoot`/WAL-replay gap; node-latch registry eviction) plus the unrelated `engine/split.Trigger` wiring item tracked separately for the concurrent Phase 2b task stream.
- `engine/split/` was explicitly untouched (concurrent agent was working on subtask 2b.1.1 in parallel); no cross-stream interference.
- No behavioral or API-breaking change: items 1-2 are comment-only; item 3 is purely additive instrumentation (one atomic increment per restart, off the latch/lock critical path, negligible overhead on an already-rare/slow path).

## Verification
- **Verdict**: PASS
- **Run ID**: 2026-07-07-002-verification
- **Details**: All 9 dimensions passed. `go build`/`go vet`/`gofmt -l` clean. `go test ./btree/... -race -count=1 -timeout 10m`: PASS, 59.97s, zero regressions. New restart-counter test re-run 5x with `-race`: 5/5 PASS, no flakiness. Diffs for `node.go`/`lookup.go` confirmed comment-only outside the single counter line each in `insert.go`/`delete.go`; `latch.go` diff purely additive. Confidence: high.

## Release Notes
Documentation-and-observability-only maintenance release for `engine/btree`. On-disk `Version` field docs and the `Tree.Lookup` lock-free doc comment now accurately describe current (post-2a.4.1) behavior. New `RestartFromRootCount()` accessor exposes retry-storm visibility with zero change to existing retry/backoff semantics. No breaking API change.
