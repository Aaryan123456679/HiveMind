---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `engine/mvcc/`

Status: scaffold only (`engine/mvcc/doc.go` placeholder). See [HLD.md](../HLD.md) for system
context.

## Purpose

Per-file multi-version concurrency control, so readers are never blocked by concurrent writers and
never see torn reads.

## Write path

- Writes create a new immutable version file, `<fileID>.vN.md`, under `content/`.
- An atomic CAS swaps the "current version" pointer in the [catalog](catalog.md) record for that
  `fileID` once the new version is durably written.

## Read path

- Readers snapshot the current version pointer at the start of the request and read that specific
  version to completion, regardless of concurrent writers advancing the pointer afterward.

## Garbage collection

- Old versions are reclaimed by a background compactor once no in-flight reader still holds a
  snapshot referencing them.
- Uses reference-counted snapshot epochs — a simplified Postgres-vacuum-style visibility scheme:
  each snapshot increments an epoch's refcount on start and decrements on completion; a version is
  eligible for GC once its epoch's refcount reaches zero and it is not the current version.

## Interactions with other modules

- `catalog/` — the CAS'd "current version" pointer lives in the catalog record.
- `split/` — during a split, the file being split stays readable at its pre-split version via
  MVCC while the split transaction assembles new files; see [split.md](split.md) for how
  `SPLITTING` status interacts with in-flight readers.
- `wal/` — every version-pointer CAS is a catalog mutation and therefore goes through the WAL
  first.

## Known risks

- **Auto-split correctness under concurrency** — MVCC is what allows split to proceed without
  blocking readers, but the split transaction itself is the highest-risk correctness surface in
  the engine. See [split.md](split.md).

## Cross-references

- [HLD.md](../HLD.md)
- [catalog.md](catalog.md) — current-version pointer + status field
- [split.md](split.md) — split transactions built on top of MVCC snapshot isolation
- [wal.md](wal.md) — durability for version-pointer swaps
