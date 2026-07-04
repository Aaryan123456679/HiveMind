# Architecture discovery — 1.4.3

Index-first: `.cdr/index/file.jsonl` entry for `engine/catalog/content.go` shows
features `content-store, content-md-file-create-write, content-md-file-read,
wal-before-apply-content, v1-single-version-pre-mvcc`, last touched by
1.4.2 run 049-implementation.

Read `engine/catalog/content.go` (1.4.1 Create + 1.4.2 Read):
- `ContentStore{dir, cat *Catalog, w *wal.Writer}`.
- `Create(rec CatalogRecord, data []byte) (int64, error)` thin wrapper over
  `createWithHook(rec, data, nil)`: WAL-append-and-apply pattern
  (`wal.AppendAndApply(cs.w, walRec, applyFn)`) where applyFn writes the
  content file then calls `cs.cat.Put(rec)`. WAL record built via
  `wal.NewCatalogPutRecord(rec.FileID, encoded)` where `encoded = rec.Encode()`.
- `Read(fileID)`: resolves via `cs.cat.Get(fileID)` first (catalog is source of
  truth / existence check), wraps `ErrNotFound`, then `os.ReadFile(ContentPath)`.
- `writeContentFile`: write-to-temp + rename (atomic, crash-safe).
- `ContentPath(fileID)` = `<dir>/<fileID>.v1.md`.

Read `engine/catalog/record.go`: `CatalogRecord` already has `SizeBytes uint64`
field (used by 1.4.1/1.4.2 tests, populated by caller currently). No append
tracking exists yet — Append must read existing record via `cat.Get`, compute
new size = old content len + len(newData), update `SizeBytes` in an updated
copy of the record, WAL-log a new catalog-Put with the updated record (same
WAL-before-apply discipline as Create), then physically append bytes to the
content file, then `cat.Put(updatedRec)`.

Read `docs/LLD/split.md`: scaffold-only `engine/split/` package. "Trigger: File
size exceeds tunable threshold (default ~8KB / ~2000 tokens) immediately
[after] append." Split package is NOT implemented yet (doc.go placeholder) —
this subtask must NOT call into engine/split. It only needs to emit a signal
(return value) so a future Epic 2B caller can act on it. No channel/callback
infra documented for this signal in split.md; simplest is a boolean return
value from Append, consistent with Create/Read's simple direct-return style
(no over-engineering, per subtask instructions).

Decision: add a package-level const `defaultSplitThresholdBytes = 8 * 1024`
plus a `ContentStore.splitThresholdBytes` field (defaulted in
`OpenContentStore`, overridable via unexported field for tests, matching
"configurable... don't over-engineer" instruction). `Append` returns
`(thresholdCrossed bool, err error)`; thresholdCrossed is true iff
oldSize <= threshold < newSize (crossing edge only, fires exactly once).
