# Plan — 2a.2.3

Add `TestGCUnderConcurrency` to `engine/mvcc/gc_test.go`.

## Setup
- `t.TempDir()`, `NewVersionWriter(dir)`, `newTestCatalog(t)`, `newTestWAL(t, dir)`,
  `NewEpochManager()`.
- Single shared fileID, seeded via `cat.Put` with `CurrentVersion: 0`.
- Seed one initial committed version before starting goroutines so readers always
  have something to snapshot from goroutine start.

## Goroutines (run concurrently, coordinated via one `sync.WaitGroup` + a `stop`
channel / duration budget so the test finishes in a few seconds):

1. **Writers** (a handful, e.g. 4): loop committing `CommitVersion` calls with
   distinct, self-describing payloads (encode fileID/writer-id/counter into the
   payload bytes so any mismatch is diagnosable) until `stop` is closed or an
   iteration budget is hit.

2. **Long-running readers** (e.g. 6, each doing several rounds): each round:
   - `NewSnapshot` -> record `snap.Version()`.
   - Immediately `Read()` once to capture "expected" content for this version
     (safe: version files are immutable once written, per read.go's doc
     comment — reading right after acquiring only ever returns that version's
     true, unchanging bytes).
   - Sleep/yield a bit (`time.Sleep` a few ms, or loop several more `Read()`
     calls) while writers and the compactor keep running concurrently.
   - `Read()` again; assert no error and exact byte-equality against the
     first read. Any error or mismatch is recorded via a mutex-guarded
     `[]string` (or `t.Errorf` directly, which is goroutine-safe in modern
     Go testing) rather than immediately failing the goroutine (so all
     failures across all readers get surfaced, not just the first).
   - `Close()` the snapshot; check its error.

3. **Compactor**: loop `RunCompaction(cat, vw, em, fileID)` back-to-back until
   `stop` is closed, checking for unexpected (non-nil, not just "nothing to
   delete") errors.

## Teardown / assertions
- `wg.Wait()`.
- Assert the shared failure collection is empty (`t.Fatalf` with all
  collected messages if not).
- Optionally assert final current version's content is still readable via one
  last `SnapshotRead`, as a sanity check that the store isn't left corrupted.

## Sizing
Keep it in the same ballpark as `TestConcurrentReadersWriters` /
`TestCurrentVersionCAS` (tens of goroutines, hundreds of ops, a few hundred ms
to a couple seconds wall time) — enough concurrency to have a real chance of
tripping a regression of the 2a.2.2 TOCTOU class, without ballooning runtime.
