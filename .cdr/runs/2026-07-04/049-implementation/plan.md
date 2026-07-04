# Plan — subtask 1.4.2 (full-file content read path)

## Decision: catalog-first read, not disk-first
`Read(fileID uint64) ([]byte, error)` resolves the fileID through `cs.cat.Get(fileID)`
first (same source-of-truth pattern `Create` uses for catalog visibility), and only
then reads `cs.ContentPath(fileID)` from disk. Rationale:
- Consistent with 1.4.1's precedent that the catalog record is the authoritative
  existence check for a fileID (mirrors `cat.Get`/`ErrNotFound` semantics already
  established and tested in `catalog_test.go` and used inside `content_test.go`'s
  hook).
- Cheap: `cat.Get` is an in-memory/page-cache lookup, not an extra disk seek beyond
  what we already need for the content file itself.
- Avoids ambiguity for a fileID that was never created (no catalog record) vs. one
  whose catalog record exists but whose content file is missing/corrupted (the
  latter treated as an internal/unexpected error, not `ErrNotFound`, since it would
  indicate a bug elsewhere, e.g. WAL replay not yet implemented — out of scope here).
- Does NOT re-derive path from `rec` fields (no per-version path in CatalogRecord
  yet); `ContentPath(fileID)` remains the fixed v1-only path per 1.4.1's docs. This
  keeps `Read` symmetric with `Create`/`ContentPath` and defers any version-aware
  path resolution (rec.CurrentVersion-keyed) to the later MVCC subtask, matching
  content.go's existing "pre-MVCC single-version only" scoping comment.

## Error handling
- fileID not in catalog: return `nil, fmt.Errorf("catalog: content read: %w: fileID %d", ErrNotFound, fileID)`
  — same wrapped-ErrNotFound convention as `catalog.go`'s `Get`/`Delete`.
- Catalog record exists but content file missing/unreadable: return a distinct
  wrapped error (not ErrNotFound) so callers can tell "never created" apart from
  "internal inconsistency".

## Test additions (content_test.go)
- `TestContentRead`: write via `cs.Create(rec, data)`, then `cs.Read(fileID)`,
  assert byte-for-byte equality (`bytes.Equal` or string compare) with `data`.
- `TestContentReadNotFound`: `cs.Read(someUnusedFileID)` returns wrapped `ErrNotFound`
  and nil data.

## Self-consistency gates
- `gofmt -l`, `go vet ./engine/catalog/...`, `go build ./...`
- `go test ./engine/catalog/... -race -v -count=1` green, including pre-existing
  `TestContentCreate` / `TestContentCreateInvalidFileID` (no regression).
