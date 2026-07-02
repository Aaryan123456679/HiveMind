---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `engine/catalog/`

Status: scaffold only (`engine/catalog/doc.go` placeholder). See [HLD.md](../HLD.md) for system
context.

## Purpose

On-disk metadata catalog for every file (topic document) HiveMind manages. This is the source of
truth for a file's identity, current version, size, and lifecycle status, and the anchor point
that `mvcc/`, `split/`, and `btree/` all coordinate through.

## Storage layout

- Slotted 4KB pages (Postgres/SQLite-style layout), stored at `.meta/catalog.dat`.
- A free-list page for reclaiming deleted/merged slots.

## Record shape

Each catalog record holds:

```
fileID          uint64   // monotonically increasing, atomic counter
pathHash
currentVersion
sizeBytes
status          ACTIVE | SPLITTING | SPLIT | REDIRECT
redirectTargetIDs []
parentTopicID
lastModified
```

`fileID` allocation is a monotonically increasing atomic counter — no reuse, no gaps-matter
semantics.

## Concurrency

Striped mutexes (~256 stripes, hashed by `fileID`) instead of one global lock, so unrelated files
never contend on the same lock.

## Interactions with other modules

- `mvcc/` performs an atomic CAS on `currentVersion` when a write commits.
- `split/` transitions a record's `status` to `SPLITTING`, then to `SPLIT`, and manages
  `redirectTargetIDs` for the redirect/stub left behind at the old path.
- `btree/` maps topic path strings to `fileID`; the catalog is keyed by `fileID` and is the
  record-of-truth once a path resolves.
- `wal/`: every catalog mutation must be logged in the WAL before being applied (see
  [wal.md](wal.md)).

## Known risks

- **Section-index staleness**: the markdown header-offset cache used by `ReadPartial` must be
  invalidated atomically within the same append/split transaction that changes a catalog record —
  otherwise `ReadPartial` can serve offsets against stale content. See [split.md](split.md).

## Cross-references

- [HLD.md](../HLD.md) — system-level architecture
- [mvcc.md](mvcc.md) — versioning built on top of catalog records
- [split.md](split.md) — status transitions during auto-split
- [wal.md](wal.md) — durability guarantee for catalog mutations
- [btree.md](btree.md) — path -> fileID lookup that resolves into the catalog
