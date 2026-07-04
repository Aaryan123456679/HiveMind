# Architecture discovery — task-2a.1.5

Index-first, then targeted reads (no source read before indexes exhausted):
- `.cdr/index/task.jsonl` — confirmed 2a.1.1-2a.1.4 all state "verified"; 2a.1.4's entry
  explicitly notes "subtask 2a.1.5 pending" and CommitVersion now requires `*wal.Writer`.
- `.cdr/index/file.jsonl` / prior handoffs (2026-07-04-075/076) — pointed at
  `engine/mvcc/write.go` and `engine/mvcc/read.go` as the two files this test integrates.

Read `engine/mvcc/write.go`:
- `VersionWriter.WriteVersion` (per-fileID monotonic numbering, never reuses/rewrites a
  version number, `states sync.Map` per-fileID stripe lock).
- `VersionWriter.CommitVersion(cat *catalog.Catalog, w *wal.Writer, fileID uint64, data
  []byte) (uint64, error)` — WAL-before-apply CAS wiring from 2a.1.4. Doc comment on
  `commitVersionWithHook` spells out the exact "no lost updates" contract for N
  concurrent `CommitVersion` calls on the SAME fileID: every call's data is durably
  written to its own never-reused version file regardless of whether its CAS wins or
  loses; a losing CAS retries with a FRESH `WriteVersion` call (never reuses the old
  version number); therefore total on-disk version files for a fileID can EXCEED the
  number of successful `CommitVersion` calls under contention (losing attempts orphan a
  version file), and the final `CurrentVersion` always equals the highest version number
  on disk once all calls have returned.

Read `engine/mvcc/read.go`:
- `NewSnapshot(cat, vw, fileID) (*Snapshot, error)` pins `CurrentVersion` at one instant;
  `Snapshot.Read()` reads that pinned version's immutable content file — no locking
  needed at read time because version files are write-once/never-rewritten and nothing
  in this codebase deletes old version files yet (no GC implemented).
- `SnapshotRead(cat, vw, fileID)` — one-shot convenience combining both.

Read `engine/mvcc/write_test.go` for reusable test helpers (avoid duplicating):
- `newTestCatalog(t) *catalog.Catalog` — fresh catalog.Catalog over `t.TempDir()`.
- `newTestWAL(t, dir) (*wal.Writer, string)` — fresh wal.Writer rooted at `<dir>/wal`.
- `countVersionFiles(t, vw, fileID) (count int, maxVersion uint64)` — scans on-disk
  `<fileID>.vN.md` files.
- `TestCurrentVersionCAS` (existing, in write_test.go) already exercises N concurrent
  `CommitVersion` calls on one fileID with NO readers — this task's new test in
  `mvcc_test.go` is the first to add concurrent READERS racing those writers via
  `NewSnapshot`/`Read`, which is the actual integration surface named in the subtask
  ("not unit-testing either in isolation").

## Design decision: avoiding a race in the test itself

The requirement flags a subtle risk: if readers check membership against a shared
"committed so far" registry that writers populate, there's a race between "commit" and
"register" — a reader could observe content whose registration hasn't happened yet
(false negative) or, if registering before committing, could observe a snapshot at a
version whose registry entry exists but whose file isn't durably readable yet (unlikely
here since WriteVersion fsyncs+renames before CommitVersion's CAS, but still an
avoidable footgun).

Resolution: every writer's entire payload sequence is generated deterministically BEFORE
any goroutine starts (keyed by `(writerID, seq)`), producing a fixed, read-only
`validSet` map used by readers. No goroutine ever mutates this map during the race, so
there is no dynamic-registration race to reason about at all — membership checking
requires no synchronization, and a read's content either exactly matches one
precomputed, immutable string or it doesn't (a torn/partial/mixed read would essentially
never coincidentally match, especially with the writer/seq/filler-padded payload shape).
