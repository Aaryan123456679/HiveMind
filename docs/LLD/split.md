---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `engine/split/`

Status: scaffold only (`engine/split/doc.go` placeholder). See [HLD.md](../HLD.md) for system
context.

**This is the highest-risk correctness surface in the entire engine.** Any change here needs a
dedicated concurrent race test before being considered done — see "Known risks" below.

## Purpose

Automatically splits a topic file into multiple topic-coherent files once it grows too large,
keeping topics well-scoped as the corpus grows.

## Trigger

File size exceeds a tunable threshold (default ~8KB / ~2000 tokens) immediately after an append.

## Split sequence

1. Mark the file `SPLITTING` in the [catalog](catalog.md). Existing readers still see the old
   version via [MVCC](mvcc.md); new writers to this specific file are queued.
2. Call the Python ingestion agent's `ProposeSplit(fileContent)` RPC (see
   [ingestion-agent.md](ingestion-agent.md)) for a topic-coherent split plan:
   `[{newPath, sectionRanges}, ...]` plus a redirect summary.
3. Atomically:
   - Allocate new `fileID`s.
   - Write the new `.md` files.
   - Write a redirect/stub at the old path.
   - Update catalog entries for all affected files.
   - Add `SPLIT_SIBLING` graph edges between the new files (see [graph.md](graph.md)).
   - Re-point or leave inbound edges pointing at the redirect stub — deliberately simpler than
     rewriting a potentially large inbound-edge list.
4. Commit as a single WAL-covered transaction, fsynced before the split becomes visible, then
   release queued writers.

## Concurrency control

A CAS-based `splitInProgress` flag, scoped per-file, ensures exactly one split wins per threshold
crossing even when many concurrent writers are appending to the same file simultaneously.

## Interactions with other modules

- `catalog/` — status transitions (`ACTIVE` -> `SPLITTING` -> `SPLIT`/`REDIRECT`), new records for
  split-off files.
- `mvcc/` — old-version readers are unaffected by an in-flight split.
- `btree/` — new topic paths inserted, old path repointed to a redirect stub.
- `graph/` — `SPLIT_SIBLING` edges added between split-off files; inbound edges retargeted to the
  redirect stub rather than rewritten en masse.
- `wal/` — the entire split is one WAL-covered, fsynced transaction.
- `agents/ingestion/` — `ProposeSplit` RPC supplies the actual split plan; the engine only
  executes it.

## Known risks

- **Auto-split correctness under concurrency**: needs a dedicated concurrent race test — many
  goroutines appending to the same file simultaneously — asserting: no data loss, exactly one
  split per threshold crossing, and no dangling graph edges. Must run under `go test -race` per
  the engine-wide convention in [AGENT.md](../../AGENT.md).
- **Section-index staleness**: the markdown header-offset cache used for `ReadPartial` must be
  invalidated atomically within the same split transaction that rewrites file boundaries.

## Cross-references

- [HLD.md](../HLD.md)
- [catalog.md](catalog.md), [mvcc.md](mvcc.md), [btree.md](btree.md), [graph.md](graph.md),
  [wal.md](wal.md)
- [ingestion-agent.md](ingestion-agent.md) — `ProposeSplit` RPC implementation
