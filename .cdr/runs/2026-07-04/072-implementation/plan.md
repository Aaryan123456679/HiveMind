# Plan — 2a.1.3 Snapshot-read path

## API shape

`engine/mvcc/read.go`, package `mvcc`:

```go
type Snapshot struct {
    vw      *VersionWriter
    fileID  uint64
    version uint64
}

// NewSnapshot captures fileID's CurrentVersion from cat AT THIS MOMENT (the
// "snapshot" instant) and returns a Snapshot pinned to that version. It does
// not read any content yet — Read does that.
func NewSnapshot(cat *catalog.Catalog, vw *VersionWriter, fileID uint64) (*Snapshot, error)

// Version returns the version number this Snapshot is pinned to.
func (s *Snapshot) Version() uint64

// Read reads THIS Snapshot's pinned version's content file to completion,
// regardless of whatever CurrentVersion has advanced to by the time Read is
// called or while it runs.
func (s *Snapshot) Read() ([]byte, error)

// SnapshotRead is a one-shot convenience: capture a snapshot of fileID's
// current version and immediately read it. Equivalent to
// NewSnapshot(cat, vw, fileID) followed by Read().
func SnapshotRead(cat *catalog.Catalog, vw *VersionWriter, fileID uint64) ([]byte, error)
```

Internal test-only seam (mirrors `content.go`'s `createWithHook` convention):

```go
// readWithHook is Read's real implementation; if afterSnapshotBeforeRead is
// non-nil, it is invoked after the version number is already pinned (snapshot
// already captured in NewSnapshot) but before the version file's bytes are
// actually read from disk — the exact window a concurrent CommitVersion needs
// to race into, to prove the read still returns pre-race content.
func (s *Snapshot) readWithHook(afterSnapshotBeforeRead func()) ([]byte, error)
```

## Correctness reasoning (confirmed, see architecture-discovery.md)

No locking is needed on the read side. `NewSnapshot` calls `cat.Get(fileID)` once, capturing
`CurrentVersion` at that instant — this IS the snapshot moment the acceptance criteria describes.
`Read`/`readWithHook` then does `os.ReadFile(vw.VersionPath(fileID, s.version))` against that pinned
version number. Because:

1. Version files are immutable once written (`WriteVersion` in `write.go` always assigns a brand-new,
   never-before-used N; it never reopens an existing N's path to rewrite it — 2a.1.1's guarantee).
2. Nothing in this codebase deletes old version files yet (epoch-based GC is explicitly deferred to
   subtask 2a.2 per the task brief; `grep -r epoch engine/` currently finds nothing).

...the file at `VersionPath(fileID, s.version)` is guaranteed to exist, and be byte-for-byte unchanged,
for the entire duration of the read — even if a concurrent `CommitVersion` call is racing to write
version N+1 and CAS `CurrentVersion` forward at the same moment. That concurrent writer touches only
its own new file and the catalog's `CurrentVersion` field; it never touches version N's already-written
file. So "genuine read-to-completion against a stale-but-consistent version" falls out for free from
the write-side immutability contract already verified in 2a.1.1/2a.1.2 — no new synchronization
primitive is required here.

Edge case: if `CurrentVersion == 0` (record exists but no version ever committed via `CommitVersion`),
`Read` will simply fail with a "file not found" wrapped error, since `VersionPath(fileID, 0)` was never
written by `WriteVersion` (which always starts at N=1). This is treated as an expected error, not a
special-cased branch — consistent with `write.go`'s existing style of surfacing OS errors via `%w`
rather than adding defensive special cases the acceptance criteria didn't ask for.

## Test plan — `engine/mvcc/read_test.go`

`TestSnapshotRead`:
1. Build a `VersionWriter` + `Catalog`, seed a `CatalogRecord`, `CommitVersion` an initial "v1" content
   so there's a real pre-existing version to snapshot.
2. Call `NewSnapshot` to capture the pointer (version 1).
3. Use `readWithHook` with a hook that: signals a channel ("paused, about to read v1's file"), then
   blocks until a second channel signals "writer has committed v2" — mirroring
   `TestFsyncBeforeApply`'s / `TestContentCreate`'s before/after channel-handoff technique.
4. Concurrently (in a goroutine), once the "paused" signal arrives, call `CommitVersion` with new
   ("v2") content — advancing `CurrentVersion` to 2 — then signal "committed" back to the paused read.
5. Assert the resumed `Read` call returns the ORIGINAL v1 content, not v2's, even though
   `cat.Get(fileID).CurrentVersion` is now 2 by the time `Read` actually returns.
6. Also assert `Snapshot.Version()` still reports 1, and that a FRESH `SnapshotRead` call made AFTER
   the v2 commit correctly observes v2 (sanity check that snapshotting isn't just permanently frozen —
   only the ALREADY-taken snapshot is pinned, not future snapshots).

Race-safety: use `-race`; the only shared mutable state touched is the catalog (already safe per
2a.1.2) and disk files (each goroutine only ever writes its own never-reused version file / reads an
already-finalized one), so no data race is expected.
