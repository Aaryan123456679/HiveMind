# Plan — Subtask 4.5.14.4

1. Read `engine/wal/writer.go`'s `ReadSegment` and `parseSegmentRecords`, and
   `engine/wal/recovery.go`'s `readSegmentFrom`, in full (done during
   architecture discovery) to confirm the exact current duplication before
   touching anything.
2. In `writer.go`, replace `ReadSegment`'s body (`os.ReadFile` +
   `parseSegmentRecords(data, 0)` + manual error wrapping) with a direct
   delegation: `records, _, err := readSegmentFrom(path, 0); return records,
   err`. Preserve the exported signature `func ReadSegment(path string)
   ([][]byte, error)` exactly.
3. Update `ReadSegment`'s doc comment to describe it as a thin wrapper around
   `readSegmentFrom(path, 0)` that discards the `tornTail` flag, while
   keeping the existing torn-tail-vs-CRC-corruption explanation (still
   accurate and still load-bearing documentation) with a pointer to
   `readSegmentFrom`/`parseSegmentRecords` for the canonical description.
4. Update `parseSegmentRecords`'s doc comment (still in `writer.go`) so it no
   longer claims to be called directly by `ReadSegment` — it's now reached
   only via `readSegmentFrom` (from both `ReadSegment` and `Replay`) and
   directly by `repairTornTail`.
5. Update `readSegmentFrom`'s doc comment in `recovery.go` to state that
   `ReadSegment` now wraps it (`readSegmentFrom(path, 0)`) rather than being
   an independent sibling implementation.
6. Do not touch `checkpoint.go`/`checkpoint_test.go` (4.5.14.3, already
   landed) or add/modify anything in `writer_test.go`'s existing tests
   (4.5.14.2, already landed) — this subtask needs no new test file since
   it's a pure refactor of an already-covered code path.
7. Verify: `go build ./wal/...`, `go vet ./wal/...`, `gofmt -l wal/*.go`,
   `go test ./wal/... -race -count=1`, plus a downstream sanity pass
   (`go test ./catalog/... ./graph/... ./mvcc/...`) since those packages call
   `wal.ReadSegment` directly.
8. Confirm via `git status`/`git diff --stat` that only `writer.go` and
   `recovery.go` changed before committing.
9. One local commit, Problem/Solution/Impact format, no push.
