# Plan — task-2a.1.5

1. Add `engine/mvcc/mvcc_test.go` with `TestConcurrentReadersWriters`:
   - Reuse `newTestCatalog`, `newTestWAL`, `countVersionFiles` from write_test.go (no
     duplication).
   - Seed one catalog record (fileID=777, CurrentVersion=0, StatusActive).
   - Precompute (before any goroutine starts) a deterministic payload per
     (writerID in [0,12), seq in [0,15)) — 180 total payloads — plus a fixed,
     read-only `validSet` map of all valid payload strings.
   - Launch 12 writer goroutines, each sequentially calling
     `vw.CommitVersion(cat, w, fileID, payloads[wID][seq])` for its 15 payloads.
   - Launch 12 reader goroutines, each looping: read an atomic "writers done" flag
     BEFORE taking a fresh `NewSnapshot`; if `Version() > 0`, `Read()` it and assert
     the bytes are an exact member of `validSet` (else `t.Errorf` — torn/partial read);
     loop until the "done" flag was already true on entry (guarantees at least one more
     read after writers finish, and genuine overlap while they're running).
   - Main goroutine: `writerWG.Wait()` -> set atomic done flag -> `readerWG.Wait()`.
   - Post-race sanity (not part of the concurrent section): `countVersionFiles` — assert
     count >= 180 (retries from lost CAS races can orphan extra version files per
     write.go's documented contract) and `maxVersion == count` (numbering is contiguous,
     never reused); final `cat.Get` CurrentVersion == maxVersion; final `SnapshotRead`
     content is in `validSet`.
2. Run `gofmt`, `go vet ./engine/mvcc/...`, `go build ./engine/...`.
3. Run `go test ./engine/mvcc/... -run TestConcurrentReadersWriters -race -v -count=1`,
   then `-count=3` and `-count=5` for flakiness, then full package
   `go test ./engine/mvcc/... -race -v -count=1` to confirm no regressions on
   2a.1.1-2a.1.4's existing tests.
4. One local commit (test-only, no production code changes).
5. Write handoff.json (pointers only) noting this closes ALL of task-2a.1 and issue #6.
6. Append `task-2a.1.5` entry to `.cdr/index/task.jsonl`.
