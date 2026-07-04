# Architecture discovery

## Indexes / docs read (index-first order)
- `.cdr/index/task.jsonl` — confirmed Phase 1 (issues #1-#5) fully verified; task-1.5.2
  is the last verified entry. This is the first mvcc task.
- `docs/HLD.md` — system context for MVCC (per-file versioning, CAS current-version
  pointer, WAL-before-apply durability).
- `docs/LLD/mvcc.md` — "scaffold only" status. Write path: "Writes create a new immutable
  version file, `<fileID>.vN.md`, under `content/`. An atomic CAS swaps the 'current
  version' pointer in the catalog record for `fileID` once the new version is durably
  written." That CAS/catalog-pointer step is explicitly out of scope for 2a.1.1 (deferred
  to 2a.1.2 per the subtask instructions) — this subtask only produces the immutable
  version file with monotonic numbering.
- `engine/catalog/record.go` — `CatalogRecord.CurrentVersion uint64` field already exists
  (added in Phase 1 as a forward-looking field per its doc comment), encoded/decoded in
  `Encode`/`Decode`. Not touched by this subtask; 2a.1.2 will wire it via CAS.
- `engine/catalog/content.go` — Phase 1's `ContentStore` is deliberately pre-MVCC,
  single-version only: always reads/writes `content/<fileID>.v1.md` regardless of
  `CurrentVersion` (see `contentVersionSuffix` and doc comments on `Create`/`Read`/
  `Append`). It reuses a striped-mutex array (`numStripes`/`stripeFor`, unexported to
  `catalog` package) to serialize read-modify-write per fileID, and a
  write-to-temp-then-rename technique (`writeContentFile`) for atomicity. Not directly
  reusable from `engine/mvcc` since `stripeFor`/`numStripes` are unexported; the pattern
  (per-fileID serialization + temp-then-rename) is replicated independently in
  `engine/mvcc`, matching the existing repo convention that `ContentStore`'s own stripes
  array is independent from `Catalog`'s (per `ContentStore`'s doc comment on why it
  doesn't share locks).
- `engine/wal/writer.go` — `Writer`/`AppendAndApply` API exists for later 2a.1.4
  integration; not used by this subtask (no catalog/WAL wiring here, per instructions).

## Design decision: monotonic numbering source
Two options considered:
1. Pure in-memory counter (map[fileID]->last version): fast, but not correct/durable
   across process restarts on its own — a fresh process would restart numbering at 1,
   colliding with existing `.v1.md`, `.v2.md` files already on disk from a prior run.
2. Derive "next N" by scanning `content/` for existing `<fileID>.v*.md` files on first
   access per fileID, taking max(existing)+1, then caching the counter in memory for
   subsequent writes within the same process (avoiding an O(dir-size) scan on every
   write).

Chose (2): correct on cold start (reflects whatever is already on disk) and fast after
warm-up. A per-fileID `sync.Mutex` (obtained via a `sync.Map` of `*fileState`) serializes
"determine next N -> write file" as one critical section, so two concurrent writers to
the same fileID can never pick the same N (satisfies "requires real synchronization"
per instructions). Different fileIDs use independent locks (no shared global lock),
consistent with the repo's striped-mutex / independent-locks convention.

Version files are immutable by construction: each write's filename embeds its own N, so
distinct writes never target the same path; prior version files are therefore left
untouched by later writes.
