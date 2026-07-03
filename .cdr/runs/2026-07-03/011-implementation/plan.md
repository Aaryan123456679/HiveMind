# Plan — Subtask 1.1.4

## `engine/catalog/idalloc.go`

- `const InvalidFileID uint64 = 0` — documented sentinel; real fileIDs start at 1.
- `const idAllocSuffix = ".idalloc"` — sidecar filename suffix appended to the catalog file's own
  path.
- `const idAllocStateSize = 8` — sidecar file holds exactly one little-endian `uint64`.
- `type IDAllocator struct { mu sync.Mutex; next uint64; stateFile *os.File }`
- `func NewIDAllocator(fm *FileManager) (*IDAllocator, error)`:
  1. Derive sidecar path from `fm.file.Name() + idAllocSuffix`.
  2. `os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)`.
  3. `Stat()`: if size == 0, high-water-mark = 0 (fresh). If size == 8, read it back
     (little-endian). Any other size is a corruption error.
  4. Return `&IDAllocator{next: hwm, stateFile: f}`.
- `func (a *IDAllocator) Next() (uint64, error)`:
  1. Lock mutex.
  2. `candidate := a.next + 1`.
  3. Persist `candidate` via `WriteAt` (8 bytes LE) + `Sync` at offset 0 of the sidecar file.
  4. On persist success, commit `a.next = candidate`; unlock; return `candidate, nil`.
  5. On persist failure, do NOT advance `a.next` (leave in-memory state consistent with last
     successfully-durable value); unlock; return `0, err`.
- `func (a *IDAllocator) Close() error` — closes the sidecar file handle (symmetry with
  `FileManager.Close`, used by tests to release the fd before reopening under the same path).

## `engine/catalog/idalloc_test.go`

One top-level `TestFileIDAllocator` (matches `-run TestFileIDAllocator`) with three `t.Run`
subtests, mirroring `file_test.go`'s style:

1. `t.Run("sequential allocation is strictly increasing from 1", ...)`
   - Fresh `t.TempDir()` catalog path, `Open()` a `FileManager`, `NewIDAllocator(fm)`.
   - Call `Next()` N times (e.g. N=50); assert each returned value is `previous+1` and the first
     is `1`.

2. `t.Run("concurrent allocation yields unique IDs, no duplicates", ...)`
   - Fresh path/FileManager/IDAllocator.
   - 100 goroutines x 100 `Next()` calls (10,000 total), collected into a mutex-guarded slice
     (or `sync.Map` keyed by ID).
   - Assert exactly 10,000 collected values and exactly 10,000 unique values (map dedupe count).

3. `t.Run("high-water-mark survives reopen, no collision", ...)`
   - Fresh path; `Open()` FileManager #1 + `NewIDAllocator` #1; allocate some IDs (e.g. 25); track
     the max ID seen; close both allocator's sidecar file and FileManager.
   - `Open()` a brand-new FileManager #2 on the *same* path + `NewIDAllocator` #2.
   - Call `Next()`; assert the new value is strictly greater than the previously-tracked max
     (specifically `max+1`, proving no gap and no collision).

## Self-consistency checks (not verification)

- `go build ./engine/...`
- `go test ./engine/catalog/... -run TestFileIDAllocator -race -v`
- `go test ./engine/catalog/... -race -v` (full package, confirm no regressions to 1.1.1-1.1.3)
