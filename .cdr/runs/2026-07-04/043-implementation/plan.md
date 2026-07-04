# Plan — 1.3.5

1. `engine/wal/writer.go`:
   - Add `parseSegmentRecords(data []byte, startOffset int) (records [][]byte, validEnd int, tornTail bool, err error)`.
   - Rewrite `ReadSegment` to call it (tolerate torn tail, keep CRC-mismatch as hard error).
   - Add `repairTornTail(path string) (validSize int64, truncated bool, err error)`.
   - Rewrite `OpenWriter`'s resuming branch to call `repairTornTail`, truncate on `truncated`, fail closed on error, before opening the file `O_APPEND`.
   - Add `func (w *Writer) Offset() int64`.
2. `engine/wal/recovery.go`:
   - Rewrite `readSegmentFrom` to call `parseSegmentRecords`, return `tornTail`.
   - Update `Replay`'s loop: hard error if `tornTail && n != lastSegment`; otherwise proceed.
   - Remove now-unused `encoding/binary` / `hash/crc32` imports.
3. `engine/wal/writer_test.go`: add `TestOpenWriterResumeTornTailDiscardsAndTruncates` (torn header case and torn payload case) closing gap (a).
4. `engine/wal/recovery_test.go`:
   - `TestCrashInjectionRecovery` (issue's literal required name): torn header tail, torn payload tail, both through `Replay`; assert no panic, valid records applied, torn bytes not replayed, clean (nil) error.
   - `TestCrashInjectionRecoveryTornTailInNonLastSegmentErrors`: defensive case.
   - `TestReplayCRCCorruption`: gap (c), flip a payload byte mid-log, assert hard error naming CRC, assert records before it were applied exactly once, assert none after.
5. `engine/wal/crash_subprocess_test.go` (new): `TestFsyncDurabilitySubprocessCrash`, gap (b), using `syscall.Kill(os.Getpid(), syscall.SIGKILL)` self-kill in a re-exec'd child, parent verifies durability after the child dies. Skip on non-unix (`runtime.GOOS == "windows"`).
6. Self-consistency: `go build ./engine/...`, `go vet ./engine/...`, `go test ./engine/wal/... -race -v -count=1`, then `-count=3` focused on the new subprocess test to check for flakiness.
7. One local commit (Problem/Solution/Impact), no push.
8. Update `.cdr/index/file.jsonl`, `.cdr/index/task.jsonl` (`task-1.3.5` -> `implemented`), write `self-consistency.json` and `handoff.json`.
