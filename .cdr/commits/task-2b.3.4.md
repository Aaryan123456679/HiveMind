# task-2b.3.4 — Graph edge-append primitive

## Summary

Fourth of 6 subtasks under GitHub issue #12 ("[2b] Atomic split-transaction
execution", Epic Phase 2b: Auto-split). Adds a new `engine/graph` package:
a minimal, append-only edge writer for `SPLIT_SIBLING`/`REDIRECT` edges. It
reuses `engine/wal`'s low-level segment writer for durable, fsynced,
CRC-checked binary encoding, but deliberately does *not* go through
`engine/wal`'s `TypedRecord`/`Replay` recovery layer — this is a standalone
low-level primitive, not yet wired into crash recovery. CSR storage,
compaction, and multi-edge traversal/query APIs are explicitly deferred to
Epic 3.

## Features

- `Edge` / `EdgeType`: fixed-shape edge record (`SourceFileID`,
  `TargetFileID`, `EdgeType` — `SPLIT_SIBLING` or `REDIRECT`).
- `EdgeAppender.AppendEdge(source, target uint64, edgeType EdgeType) error`:
  encodes and durably appends a single edge record via `engine/wal`'s
  segment writer (fsync + CRC on every append); rejects invalid
  `EdgeType` values.
- `ReadAll() ([]Edge, error)`: full sequential read-back of all appended
  edges, decoding and validating record length/CRC.

## Impact

- Subtask 4 of 6 under issue #12; issue remains open pending 2b.3.5-2b.3.6
  (inbound-edge repointing at `engine/split`'s call site, and the final
  WAL-covered atomic transaction wrapper).
- Non-blocking comments carried forward from verification (not required
  before merge, tracked follow-up):
  1. **MOST IMPORTANT** — edge-append records are durable at the byte level
     (fsynced) but are **not** integrated into any crash-recovery replay
     path the way catalog/btree records are via `wal.Replay`. This gap must
     be explicitly resolved — not silently dropped — by 2b.3.5 or 2b.3.6,
     e.g. by folding graph writes into 2b.3.6's WAL-covered transaction so
     edge appends recover consistently with the rest of the split
     transaction. Tracked as a flagged (non-deferrable) item in
     `.cdr/memory/pending.md`.
  2. `AppendEdge` rejects invalid `EdgeType` but does not separately reject
     `Source`/`Target == 0`; low risk for a minimal, currently-unwired
     primitive, but worth revisiting at 2b.3.5's call site once real
     fileIDs are threaded through.

## Verification

- Verdict: `PASS_WITH_COMMENTS`
- Run ID: `2026-07-07-022-verification`
- Commands: `go build ./...`, `go vet ./...`, `gofmt -l .`,
  `go test ./graph/... -run TestMinimalEdgeAppend -count=5 -timeout 10m -v`,
  full module `go test ./... -count=1 -timeout 20m`,
  `go test ./wal/... -race -count=1 -timeout 10m` — all green, zero
  regressions.

## Release Notes

- feat(engine/graph): add minimal append-only edge writer for
  SPLIT_SIBLING/REDIRECT edges, built on engine/wal's segment writer.
