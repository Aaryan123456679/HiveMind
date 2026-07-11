# Architecture Discovery

## Index lookups (read before source, per protocol)

- `.cdr/index/file.jsonl` line 37: `engine/wal/writer.go` — module `engine/wal`, features include
  `segment-writer`, `append-only-log`, `size-based-rotation`, `length-crc-header-framing`,
  `hard-error-on-oversized-record`, `open-writer-resume-numbering`, `read-segment-parser`,
  `shared-torn-tail-vs-crc-corruption-parser`, `resume-torn-tail-detect-and-truncate`,
  `writer-offset-getter`. last_change_run: 2026-07-04-043-implementation.
- `.cdr/index/task.jsonl` line 141: task-4.5.4.4 (issue #41), verdict PASS_WITH_COMMENTS, commit
  ab5e962. Verification confirmed the false "matches SaveRoot precedent" claim was only ever in a
  commit message (c1c89f7), never literally in checkpoint.go's doc comment history — the fix was
  an additive corrective paragraph, not a delete/reword of a literal false sentence. This is a
  useful precedent for style (additive corrective note) but NOT identical shape: unlike checkpoint.go,
  writer.go's overclaim IS literally present in the current doc comment text (verified directly,
  see below), so this fix can be a direct in-place correction rather than only-additive.
- `.cdr/index/regression.jsonl` line 152: subtask 4.5.4.1 verification report flags the general
  class of defect ("doc-comment-drift" / doc comments claiming a precedent/idiom-match that isn't
  literally accurate) as the same defect family checkpoint.go's 4.5.4.4 fixed. Confirms this is a
  known, recognized pattern in this codebase's technical debt.

## HLD / LLD (read before source, per protocol)

- `docs/HLD.md`: `engine/wal` provides durability/crash-recovery for catalog/index mutations;
  no per-file durability-idiom detail at this level (that detail lives in the LLD).
- `docs/LLD/wal.md` (post 4.5.4.5 sync, commit 7e7b97a): explicitly documents `Writer.Append`'s
  durability behavior under "Record header and rotation":
  > "`Writer.Append` writes the header, then the payload, then calls `file.Sync()` before
  > returning — every `Append` call is durable (fsynced) by the time it returns."
  This description already matches plain sequential `file.Write`+`Sync` and does NOT claim a
  `WriteAt`+`Sync` idiom-match to `engine/catalog`. It also does not mention `engine/catalog` at
  all in this section. So **no wal.md correction is needed** — the LLD is already accurate; only
  the writer.go source doc comment (which predates or diverged from the LLD sync) is wrong.
- `docs/LLD/wal.md`'s "Checkpointing" section (post 7e7b97a) already contains the corrected,
  precise wording style used for checkpoint.go's analogous fix: it explicitly says the
  temp-file+Sync+rename idiom is "not modeled on engine/btree/persist.go's SaveRoot, which
  durably persists its root node ID via a structurally weaker technique (an in-place f.WriteAt
  followed by f.Sync, with no temp file and no rename)." This is the stylistic template to match:
  name the actual mechanism precisely, name the file/idiom it was wrongly claimed to match, and
  state the real relationship (or lack thereof) explicitly.

## Commit ab5e962 diff review (checkpoint.go, issue #41 4.5.4.4)

Reviewed via `git log -p ab5e962 -1 -- engine/wal/checkpoint.go`. The fix appended an additive
paragraph after the existing doc comment, precisely naming: (a) the idiom actually used here
(temp-file+Sync+os.Rename), (b) the file/idiom it must not be conflated with
(engine/btree/persist.go's SaveRoot: in-place WriteAt+Sync, no temp file, no rename), and (c) an
explicit statement that the two are "structurally different atomic-write strategies" and the
correction "should not be read as mirroring SaveRoot's[behavior]".

## Commit 7e7b97a diff review (docs/LLD/wal.md, issue #41 4.5.4.5)

Reviewed via `git log -p 7e7b97a -1 -- docs/LLD/wal.md`. Full LLD rewrite from scaffold-only to
implemented-package description; contains the precise Append/durability wording quoted above
under "Record header and rotation", already free of the WriteAt+Sync overclaim.

## Source: engine/wal/writer.go (read directly, after index/LLD exhausted)

Lines 46-63, the `Writer` struct's doc comment, currently reads (verified via `sed -n '44,63p'` on
the raw file, confirming exact text — an earlier tool-display artifact had shown a
whitespace-stripped preview of this same text; the actual on-disk file is normal, unabbreviated
Go source):

```go
// Writer appends records to an append-only, size-rotated sequence of WAL
// segment files. It is the durability mechanism described in
// docs/LLD/wal.md: every Append fsyncs the record to disk before returning,
// matching this repo's WriteAt+Sync durability idiom (see
// engine/catalog/file.go's FileManager.WritePage and
// engine/catalog/idalloc.go's IDAllocator.Next).
```

The substantive overclaim: it claims `Append`'s fsync-before-return matches a "WriteAt+Sync
durability idiom" exemplified by `engine/catalog/file.go`'s `FileManager.WritePage` and
`engine/catalog/idalloc.go`'s `IDAllocator.Next`.

Verified `Append`'s actual implementation (lines 326-364): it calls `w.file.Write(header[:])`,
then (if payload non-empty) `w.file.Write(payload)`, then `w.file.Sync()`. This is plain
sequential `Write` (not `WriteAt`) at the current append position of an append-only,
size-rotated file — structurally different from `engine/catalog`'s `WriteAt`-based idiom, which
writes to a computed absolute offset within a fixed-layout random-access file (pages/slots), not
sequentially at end-of-file.

Confirmed via `grep` that `engine/catalog/file.go`'s `FileManager.WritePage` and
`engine/catalog/idalloc.go`'s `IDAllocator.Next` do in fact use `WriteAt`+`Sync` (this part of the
existing comment's factual claim about catalog is accurate — only the "matching"/"idiom" claim
that writer.go's own `Append` follows the same idiom is wrong).

## Conclusion

- Fix is scoped to `engine/wal/writer.go`'s `Writer` doc comment (lines ~46-51) only.
- `docs/LLD/wal.md` is already correct post-4.5.4.5; no doc sync needed for this subtask.
- Style precedent to follow: name the real mechanism (`file.Write`+`Sync`, sequential/append-only)
  precisely, name what it is NOT (`engine/catalog`'s `WriteAt`+`Sync` random-access idiom), and
  affirmatively note the sequential approach is a reasonable, deliberate choice for an
  append-only log (matching the issue's framing, "a reasonable choice ... just imprecisely worded
  today") rather than implying it's a deficiency.
