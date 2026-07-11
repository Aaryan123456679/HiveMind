# Architecture Discovery

## Index lookups (before source)

- `.cdr/index/file.jsonl`: engine/catalog/content.go features =
  ["content-store", "content-md-file-create-write", "content-md-file-read",
  "content-append-path", "sizebytes-tracking", "split-threshold-signal-stub",
  "wal-before-apply-content", "v1-single-version-pre-mvcc"];
  last_change_run = 2026-07-04-052-implementation.
- `.cdr/index/regression.jsonl` line 42 (2026-07-04/047-verification, subtask
  1.4.1, module engine/catalog, risk=low): documents the exact gap this
  subtask closes (see requirement.md "Origin").
- `docs/LLD/catalog.md`: scaffold-level LLD. Covers on-disk record shape,
  striped-mutex concurrency design, and cross-module interactions
  (mvcc/split/btree/wal). Does NOT document ContentStore.Create's
  duplicate-fileID semantics at all (out of scope for this doc, to be
  addressed by companion subtask 4.5.5.5 docs-sync, which we do not touch).
  Doc's "Known risks" section only calls out section-index (header cache)
  staleness, unrelated to this subtask.

## Source findings (engine/catalog/content.go)

- `ContentStore.Create(rec CatalogRecord, data []byte) (int64, error)` is a
  thin wrapper around `createWithHook`.
- `createWithHook`:
  1. Rejects `rec.FileID == InvalidFileID`.
  2. Encodes rec, builds a WAL CatalogPut record.
  3. `wal.AppendAndApply` durably appends the WAL record, THEN (inside the
     apply closure) calls `writeContentFile(rec.FileID, data)` followed by
     `cs.cat.Put(rec)`.
  4. No existence check anywhere in this path — no call to `cs.cat.Get` or
     `os.Stat(ContentPath(...))` before writing.
- `writeContentFile`: writes to a fresh `os.CreateTemp` file, `Sync()`s,
  `Close()`s, then `os.Rename(tmpPath, finalPath)`. `os.Rename` on POSIX
  atomically replaces the destination if it exists; the old inode's data is
  released once no fd holds it open (no fd is held open across the Create
  call), so a second Create for the same fileID leaves no leaked file.
- `Catalog.Put` (engine/catalog/catalog.go, read-only reference, NOT
  modified): explicitly documented ("Put inserts or overwrites... If a record
  already exists for fileID, Put always deletes the old slot and inserts a
  fresh one... Put overwrites the current record for a fileID, full stop —
  no history/versioning of a fileID's past records is kept") as a safe upsert.
  No leaked page slots (old slot tombstoned before reinsert).

## Conclusion feeding into plan.md

Current behavior: a second `Create` call for a fileID that already has a
record/content file performs a clean, atomic, non-corrupting overwrite of
both the content file and the catalog record. No leaked file descriptors, no
leaked catalog slots, no torn/partial state possible (write-temp-then-rename
is atomic; Catalog.Put's delete-then-reinsert is fully serialized under the
per-fileID stripe lock it takes internally). This matches the regression
note's "silently overwrites" framing — the behavior is safe, just previously
UNDOCUMENTED and UNTESTED.

Decision (recorded in plan.md): Path (a) — document the existing overwrite
behavior explicitly in Create's doc comment, and add a test pinning it down,
rather than adding a new already-exists guard (which would be a behavior
change beyond this subtask's evidence-based scope, and was not indicated as
necessary by the low-risk severity or the safe-upsert nature of the
underlying primitives).
