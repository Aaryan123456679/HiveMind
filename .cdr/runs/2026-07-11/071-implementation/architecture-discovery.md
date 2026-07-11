# Architecture Discovery — Subtask 4.5.5.3

## Read source (mandatory per protocol before implementing)
Read `engine/catalog/content.go` in full (564 lines). Key findings:

- `Append(fileID, data)` (line 336): takes `cs.stripes[stripeFor(fileID)]` for
  its ENTIRE critical section (read-existing -> append -> encode ->
  `wal.AppendAndApply` -> `writeContentFile` -> `cat.Put` ->
  `InvalidateHeaderCache`), i.e. Append is fully serialized per fileID already
  (there is no concurrent-Append-vs-Append torn write risk left to prove;
  that was subtask 1.4.3's `TestContentAppendConcurrentSameFileID`
  regression test, already in the file). What's NOT proven yet: a *Read*
  running concurrently with a stream of serialized Appends never observes a
  torn/partial byte sequence.
- `writeContentFile` (line 533): writes to `os.CreateTemp` in the same `cs.dir`
  directory as the final path, `Write`+`Sync`+`Close`, THEN `os.Rename(tmpPath,
  finalPath)`. Rename within the same directory/filesystem is atomic on POSIX,
  which is the actual mechanism providing "Read always observes either the
  fully-old or fully-new content, never torn" (per the `ContentStore` doc
  comment on `Read`, lines 62-64).
  Inside `Append`'s closure (called via `wal.AppendAndApply`), the ordering is:
  `writeContentFile` (content write) THEN `cs.cat.Put(updatedRec)` (catalog
  visibility) — i.e. content-write-before-cat.Put, exactly the second half of
  the guarantee under test.
- `Read(fileID)` (line 290): calls `cs.cat.Get(fileID)` FIRST, then
  `os.ReadFile(cs.ContentPath(fileID))`. It takes NO lock (no `cs.stripes`)
  — by design, since a single `os.ReadFile` against an atomically-renamed
  path can never itself observe a torn file, per the doc comment.
- No test-only hook exists on `Append`/`Read`/`writeContentFile` today. The
  only existing hook in this file is `createWithHook`'s
  `afterWALBeforeApply`, wired for `Create` only (used by
  `TestContentCreate` to observe WAL-durable-before-catalog-visible
  ordering). There is no analogous seam for `Append`/`Read`.

## btree hook precedent (checked per instructions)
`optimisticReadHook` (btree/lookup.go, exercised in btree_test.go:452) and
`crabRetryHook` (btree/delete.go, insert.go, exercised in
insert_test.go:787-925) are production-code seams that let a test force
execution to pause/resume at one specific, narrow, hard-to-hit internal
window (mid-way through a multi-step latch-crabbing/retry algorithm) that
would otherwise require many iterations of luck to hit at all. They are
appropriate there because the race window is internal to an in-memory,
multi-step algorithm with no natural repeated-trigger mechanism.

Content's guarantee is different in kind: it's an OS/filesystem-level
atomicity property (`rename(2)`) exercised across a straightforward
loop of many independent Append calls interleaved with many independent Read
calls. Every single Append call is itself a complete opportunity to expose a
torn read if the atomicity property didn't hold — there's no single narrow
internal window to pin with a hook; a hook would only add complexity without
narrowing anything meaningfully, since the property is "does this instant of
rename ever let a concurrent reader see garbage", which reproduces on every
one of many iterations already, at the syscall level a Go-code hook cannot
observe more finely than repetition already does.

## Decision
Statistical/iteration-based `-race` test, matching:
1. The acceptance criteria's own wording ("empirically pinning down"), which
   the issue author chose deliberately (contrast with 1.4.3's regression test
   docstring, which explicitly says "not a hook-based deterministic
   reproduction ... just the logical lost-update outcome" for a similar
   in-memory race — the same iteration-based idiom this codebase already
   uses when a hook doesn't fit).
2. The test spec's literal invocation (`-race -run
   TestContentAppendConcurrentRead`) has no hook-setup counterpart mentioned,
   unlike e.g. `insert_test.go`'s hook-based tests which the spec would
   presumably also name distinctly if a hook were required here.
3. The hard scope constraint: this session may ONLY touch
   `engine/catalog/content_test.go`; adding a hook would require editing
   `content.go` (out of scope, and no such hook exists in the current
   `Append`/`Read`/`writeContentFile` code to reuse).

## Self-contained validation technique chosen (avoids test-harness races)
Rather than have the test track "which Append committed in what order" via
external bookkeeping (which itself races against the goroutine that performs
the external bookkeeping AFTER `Append` already returns — a false-negative
trap), each appended chunk is self-describing:
`<<NNNN:{NNNN bytes of 'Z'}>>\n` (4-digit decimal length prefix, then exactly
that many literal `'Z'` bytes, then a fixed `>>\n` footer). A `parseChunks`
helper walks any `Read()` snapshot from the end of the fixed initial content
onward and validates every chunk boundary/length/footer is well-formed. A
malformed marker, wrong body length, wrong body byte, or truncated
length/footer at the tail is exactly what a torn/partial read (a
non-atomic rename racing a concurrent Read) would produce, and gets flagged
immediately — with no dependency on knowing the actual interleaving order the
concurrent Appends committed in.

Separately, the content-write-before-cat.Put ordering half of the guarantee
is checked by reading `cat.Get(fileID).SizeBytes` immediately before calling
`cs.Read`, and asserting the read content is never SHORTER than that
just-observed `SizeBytes` (size is monotonically non-decreasing across
Appends, so this holds regardless of unknown interleaving, and would fail if
`cat.Put` became visible before the content file write completed).
