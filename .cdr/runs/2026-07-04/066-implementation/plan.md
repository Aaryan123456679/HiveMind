# Plan

1. New package `engine/mvcc` with `write.go`:
   - `VersionWriter` type: `dir string` (content dir root, created if missing), plus
     `states sync.Map` (fileID uint64 -> *fileState).
   - `fileState`: `mu sync.Mutex` + `next uint64` (0 = unknown, must scan disk).
   - `NewVersionWriter(root string) (*VersionWriter, error)`: mkdir -p `<root>/content`.
   - `VersionPath(fileID, version uint64) string`: `<dir>/<fileID>.v<version>.md`.
   - `(*VersionWriter) WriteVersion(fileID uint64, data []byte) (uint64, error)`:
     get-or-create fileState for fileID (via `sync.Map.LoadOrStore`), lock its mutex,
     resolve next version (scan dir on first use, else cached+1), write via
     temp-file-then-rename into `VersionPath(fileID, next)`, update cache, unlock,
     return version number.
   - `scanLatestVersion(fileID) (uint64, error)`: glob `<fileID>.v*.md`, parse trailing
     integer, return max (0 if none found).
2. `write_test.go`: `TestVersionWriter` covering:
   - Sequential writes to one fileID assert v1, v2, v3... in order, prior files
     untouched (content + mtime).
   - Concurrent writes (many goroutines, e.g. 50) to the SAME fileID under `-race`:
     collect all returned versions, assert all distinct and form {1..N} with no gaps
     (stronger than "just strictly increasing" since each write allocates exactly the
     next integer).
   - Cold-start correctness: a second `VersionWriter` opened against the same dir
     picks up numbering from existing files on disk (max+1), not restarting at 1.
3. Self-consistency: `go build ./...`, `go vet ./...`, `gofmt -l`, and
   `go test ./engine/mvcc/... -race -v -count=1`.
4. One commit (no push). Update `.cdr/index/task.jsonl` with `task-2a.1.1` entry.
5. Handoff pointers only for `/cdr:verify`.
