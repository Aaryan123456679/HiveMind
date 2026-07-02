---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `engine/wal/`

Status: scaffold only (`engine/wal/doc.go` placeholder). See [HLD.md](../HLD.md) for system
context.

## Purpose

Write-ahead log providing durability and crash recovery for all catalog/index mutations across the
engine.

## Storage layout

- Append-only segment files: `wal/wal-<segment>.log`.
- Periodic checkpointing tracked in `manifest.json`, which records the checkpoint pointer (the WAL
  offset up to which state has been durably applied and can be truncated/archived).

## Recovery

- On startup, the engine replays the WAL from the last checkpoint pointer forward, reapplying any
  mutations that were logged but not yet reflected in the checkpointed state, to recover from a
  crash.

## Invariant

Every mutation to the catalog or any index (B+Tree, graph adjacency) must be logged in the WAL
*before* it is applied in memory or on disk. This is the durability backbone every other module
depends on:

- [catalog.md](catalog.md) — record mutations
- [mvcc.md](mvcc.md) — version-pointer CAS
- [split.md](split.md) — the entire multi-step split sequence commits as a single WAL-covered,
  fsynced transaction
- [btree.md](btree.md) — index insert/delete

## Known risks

- None unique to this module beyond the general requirement that recovery correctness be tested
  against crash-injection scenarios once the implementation exists (tracked under the engine's
  `-race` testing convention in [AGENT.md](../../AGENT.md) for the concurrent-write paths that
  feed the log).

## Cross-references

- [HLD.md](../HLD.md)
- [catalog.md](catalog.md), [mvcc.md](mvcc.md), [split.md](split.md), [btree.md](btree.md)
