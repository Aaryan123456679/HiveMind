# Plan — 1.4.4

## New production code (flagged clearly, not just test scaffolding)
`engine/catalog/recovery.go`:

```go
func RecoverFromWAL(cat *Catalog, walDir string) error
```

- Calls `wal.Replay(walDir, apply)` where `apply` switches on `rec.Type`:
  - `wal.RecordCatalogPut`: `payload, err := rec.AsCatalogPut()`; `crec, err :=
    Decode(payload.Record)`; `cat.Put(crec)`.
  - `wal.RecordCatalogDelete`: `payload, err := rec.AsCatalogDelete()`; `cat.Delete(payload.FileID)`,
    tolerating `ErrNotFound` (idempotency: a delete for a fileID never Put in this fresh index --
    can't happen in practice given WAL ordering, but don't hard-fail if it does since Replay's
    contract is "reapply every mutation", not "assert prior state").
  - Any other record type: ignore (BTree records out of scope for catalog recovery; forwards-
    compatible rather than hard-erroring, since wal.Replay itself already validates record-type
    well-formedness).
- Kept as small as possible; no new locking, no new files beyond this one, reuses
  `Catalog.Put`/`Catalog.Delete`'s existing durability/locking guarantees.

## Test: engine/catalog/content_test.go
1. `reopenTestContentStore(t, root string, walDir string) (*ContentStore, *Catalog)` helper:
   opens a NEW `FileManager` against `root/catalog.dat`, a NEW `Catalog`, calls
   `RecoverFromWAL(cat, walDir)`, opens a NEW `wal.Writer` via `wal.OpenWriter(walDir, ...)`
   (resuming append per issue #3's `OpenWriter` resume-from-disk support), wires a NEW
   `ContentStore` via `OpenContentStore(root, cat, w)`. Registers `t.Cleanup` for the new
   FileManager/Writer.
2. `TestContentDurabilityRestart`:
   - Build the first generation via `newTestContentStore`-equivalent, but capture `root` so the
     "restart" can reopen it (existing `newTestContentStore` helper hides `root`; add a small
     variant or expose root similarly to how it already exposes `walDir`).
   - `Create` a file, `Append` 2-3 times.
   - Close the ORIGINAL FileManager + wal.Writer (do NOT delete on-disk files) -- simulates
     process exit. (t.Cleanup from the first generation's helper already closes them at test
     end; call `fm.Close()`/`w.Close()` explicitly mid-test instead of relying on deferred
     cleanup, then reopen fresh handles against the same root.)
   - Reopen via the new helper, call `RecoverFromWAL`.
   - `Read` via the NEW `ContentStore`; assert byte-for-byte equality against the content
     that should be present after all appends (last-writer-wins full content, not just the
     first Create).
   - Race-safe: run under `-race`; no concurrent goroutines needed for this test itself, just
     clean sequential open/close/reopen.

## Why not modify engine/wal
Confirmed via re-reading `engine/wal/recovery.go`: `Replay`'s existing API and semantics fully
suffice (append-only ordered replay + type validation); no gap found. No changes made there.
