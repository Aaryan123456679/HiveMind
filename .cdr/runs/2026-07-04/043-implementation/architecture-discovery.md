# Architecture discovery — 1.3.5

Token order followed: `.cdr/index/*` -> `.cdr/memory/*` -> `docs/HLD.md` /
`docs/LLD/wal.md` -> the four already-verified source files in full
(`engine/wal/writer.go`, `record.go`, `checkpoint.go`, `recovery.go`) ->
their existing tests.

## Current on-disk parsing behavior (before this subtask)

Both `Writer.go`'s `ReadSegment` (full-file parse from offset 0) and
`recovery.go`'s `readSegmentFrom` (parse from an arbitrary offset, used by
`Replay`) already implement **identical** integrity checks, duplicated as
near-identical loops:

- truncated header (fewer than `recordHeaderSize` bytes remain) -> hard error
- truncated payload (declared length exceeds remaining bytes) -> hard error
- CRC32 mismatch on a full-length payload -> hard error

`OpenWriter`'s resume path does none of this: when resuming, it just
`Stat()`s the existing segment file for its current size and opens it
`O_APPEND`, with a doc comment explicitly flagging this as deferred to
1.3.4/1.3.5 ("does not validate the resumed segment's tail for
torn/partial records"). This is gap (a): if the file's tail is torn, new
appends land after the torn bytes, and both `ReadSegment` and
`readSegmentFrom` (parsing sequentially from offset 0 / a checkpoint offset)
would hit the torn bytes before ever reaching the newly-appended valid
records, hard-erroring out of what should be a normal, recoverable resume.

## Why "torn" and "CRC-mismatch" must be handled differently

A crash mid-`Append` can only ever leave a **short** write: the process dies
somewhere between writing the 8-byte header and finishing the payload write
(and, per the code, `Sync()` is only called after both writes succeed, so a
crash before `Sync` returns leaves, at worst, a short file — never a
full-length record with corrupted bytes, since nothing overwrites
already-written bytes). A CRC32 mismatch on a *full-length* record therefore
cannot be produced by an incomplete write; it indicates genuine bit-level
corruption (bad disk sector, backup/restore bug, manual tampering, etc.) —
a fundamentally different, more serious failure that must stay loud.

The issue's literal acceptance criteria ("torn record is detected and
discarded, recovery proceeds") governs the torn case specifically. Gap (c)'s
wording ("clear error... not silent data corruption") governs the CRC case
specifically. Both are satisfiable, consistently, by treating them as two
distinct outcomes of one shared low-level parser rather than one blanket
error-or-not decision.

## Design chosen

Factor both `ReadSegment`'s and `readSegmentFrom`'s parsing loops into one
shared, package-private `parseSegmentRecords(data []byte, startOffset int)
(records [][]byte, validEnd int, tornTail bool, err error)`:

- truncated header/payload at the tail -> `tornTail=true`, `err=nil`,
  `records`/`validEnd` reflect everything parsed before the torn bytes.
- CRC mismatch -> hard `err`, `records`/`validEnd` reflect everything parsed
  strictly before the corrupt record (this is unchanged behavior).
- Clean end-of-data -> `tornTail=false`, `err=nil`.

Callers:

- `ReadSegment` (unexported-context-free, single segment, always starts at
  offset 0): tolerates a torn tail (returns `records, nil`, discarding
  `tornTail`/`validEnd`, since a standalone full-file read has no broader
  "is this the last segment" context to reason about). Unchanged behavior
  for CRC corruption (still errors, as before). No existing test constructs
  a genuinely torn segment, so this is behavior-preserving for everything
  currently tested, while satisfying 1.3.5's requirement going forward.
- `OpenWriter`'s resume path: before reopening the resumed segment for
  append, reads it once, calls `parseSegmentRecords`, and:
  - if `tornTail`, truncates the on-disk file to `validEnd` (physically
    discarding the torn bytes) before opening for append — this is the
    "detected and discarded" half of the acceptance criteria, applied at
    resume time rather than only at replay time.
  - if a CRC-mismatch error is returned, `OpenWriter` fails closed (returns
    a clear, wrapped error) rather than resuming onto a segment already
    known to be corrupt — this is gap (a)'s "at minimum returns a clean
    error" floor, but implemented as the stronger detect+truncate behavior
    the orchestrator's guidance called "preferred".
- `readSegmentFrom` / `Replay`: gains the same `tornTail` signal. `Replay`
  additionally enforces that a torn tail is only tolerated in the
  **last** (highest-numbered) segment — the only segment a crash could
  plausibly have left mid-write. A torn tail found in an earlier segment is
  treated as a hard on-disk inconsistency (defensive; this shouldn't be
  reachable via this package's own write path, but guards against silently
  masking real corruption elsewhere in the log).

This makes `OpenWriter`'s resume logic and `Replay`'s parsing consistent by
construction (same shared parser, same torn-vs-corrupt distinction), which
directly closes gap (a) and satisfies the "make it consistent" requirement
from the orchestrator's design guidance.

## Gap (b): fsync-durability, real process boundary

`Writer` is hard-coded to `*os.File` (no injected-file seam), so the
"counting/intercepting file wrapper" option is not available without a
larger refactor that is out of proportion for this subtask. Implemented the
subprocess-kill technique instead: a re-exec of the same test binary
(`os.Args[0]` with `-test.run=^TestFsyncDurabilitySubprocessCrash$` and an
env-var flag) opens a `Writer`, `Append`s one record, and — immediately after
`Append` returns control — sends itself `SIGKILL` via `syscall.Kill`. The
parent process waits for the child to die (asserts it was in fact killed,
not merely exited zero), then reopens the same WAL directory in-process and
confirms the record is present and intact.

This is documented (in the test's own doc comment and in this run's
handoff) as proving: no in-Go buffering delays the record past the point
`Append` returns (a `SIGKILL`self-signal cannot be caught, deferred, or
finalized, so if the bytes are recoverable afterward from a *separate*
process's read, they were truly on the file already, not sitting in some
process-local buffer that a graceful exit would have needed to flush). It
does **not** prove the kernel's own page cache was flushed to the physical
disk platter/controller (genuine `fsync`-to-hardware durability requires
power-loss/VM-level testing, out of scope here) — that limitation is stated
explicitly in the test's doc comment, per the orchestrator's request to
document the rigor level either way.

## Gap (d): `Writer.Offset()`

Added a single, minimal getter, `func (w *Writer) Offset() int64`, returning
the same mutex-guarded `size` field `SegmentNum()` already exposes a
sibling of. No other API surface changes. Justification: a checkpoint caller
needs `Checkpoint(dir, uint64(w.SegmentNum()), <offset>)`, and today there is
no way to obtain `<offset>` from `Writer` itself (it can only be recovered
from each `Append`'s own return value, which is awkward for a caller that
wants to checkpoint at an arbitrary later point, e.g. from a separate
goroutine/ticker). This getter is used directly by this subtask's own crash
tests.

## Files touched

- `engine/wal/writer.go` — `parseSegmentRecords` (new, shared), `ReadSegment`
  (rewritten in terms of it, same signature/behavior for all pre-existing
  tests), `OpenWriter` (resume path now validates + truncates a torn tail,
  or fails closed on real corruption), `Writer.Offset()` (new getter).
- `engine/wal/recovery.go` — `readSegmentFrom` (rewritten in terms of
  `parseSegmentRecords`, gains a `tornTail` return), `Replay` (torn-tail
  handling: tolerated only in the last segment). Unused `encoding/binary`
  and `hash/crc32` imports removed (logic moved into writer.go's shared
  helper).
- `engine/wal/writer_test.go` — new test closing gap (a) at the `OpenWriter`
  layer.
- `engine/wal/recovery_test.go` — `TestCrashInjectionRecovery` (the issue's
  literal required test name), plus a CRC-corruption-through-`Replay` test
  closing gap (c), plus a torn-tail-in-non-last-segment defensive test.
- `engine/wal/crash_subprocess_test.go` (new) — the subprocess-kill
  fsync-durability test closing gap (b).
