# Plan — task-1.4.1

1. Create `engine/catalog/content.go`:
   - `ContentStore` struct wrapping a content-directory root path, a `*Catalog`, and a
     `*wal.Writer`.
   - `OpenContentStore(root string, cat *Catalog, w *wal.Writer) (*ContentStore, error)`:
     `os.MkdirAll(filepath.Join(root, "content"), 0o755)`.
   - `ContentPath(fileID uint64) string`: returns `content/<fileID>.v1.md` under the store's root
     (hardcoded literal "v1" — pre-MVCC, single-version scope of this subtask).
   - `Create(rec CatalogRecord, data []byte) (int64, error)`: the public create/write path.
     Encodes `rec`, builds a `wal.NewCatalogPutRecord(rec.FileID, encoded)`, and calls
     `wal.AppendAndApply(w, walRec, apply)` where `apply` writes the content file to disk
     (via `os.WriteFile` to a temp name + rename for atomicity, or direct WriteFile — decide
     during implementation) and then calls `cat.Put(rec)`. Returns the WAL offset.
   - Internal seam `createWithHook(rec, data, afterWALBeforeApply func())` used by `Create` (hook
     nil in production) so `content_test.go` can observe WAL-durable-but-catalog-not-yet-visible
     state from inside the same package, mirroring `engine/wal/record_test.go`'s
     `TestFsyncBeforeApply` proof technique.
   - Guard: reject `rec.FileID == InvalidFileID`.
2. Create `engine/catalog/content_test.go`:
   - `TestContentCreate`: build a temp dir, `Open` a FileManager + `NewCatalog`, `wal.OpenWriter`,
     `OpenContentStore`; call `Create`; assert content bytes on disk equal input; assert
     `cat.Get(fileID)` now returns the record; assert the WAL segment (`wal.ReadSegment`) contains
     a decodable `RecordCatalogPut` for the fileID.
   - Ordering assertion: use `createWithHook` with a hook that reads the WAL segment (must already
     contain the record) and checks `cat.Get(fileID)` still returns `ErrNotFound` at that point —
     proving WAL-append precedes catalog visibility, the same before/after observation technique
     `TestFsyncBeforeApply` uses.
3. Run `gofmt`, `go vet`, `go build ./...`, `go test ./engine/catalog/... -race -v -count=1`.
4. `self-consistency.json`, one commit, `handoff.json`, task.jsonl entry.
