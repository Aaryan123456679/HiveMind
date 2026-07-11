# Architecture Discovery — Subtask 4.5.4.2

**File read fresh:** `engine/wal/record.go` (full file, 558 lines) as of current HEAD.

**Finding — confirmed exact lines (differ slightly from issue's approximate estimate,
as expected since the file has shifted since filing):**

- Lines 1-340: package doc, `RecordType` enum, `String()`, `TypedRecord`
  Encode/Decode, and the CatalogPut / CatalogDelete / BTreeInsert / BTreeDelete
  sections (each with its own `--- Foo ---` header, doc comments, Encode/Decode,
  constructor, and `AsFoo` accessor) — all correct, untouched by this subtask.

- **Lines 342-369: the orphaned/duplicated doc-comment block.** Starts with
  `// --- fsync-before-apply write path ---` (line 342) immediately followed (no
  blank separator — the tell) by the full `AppendAndApply` doc comment (lines
  344-369, ending "...also roll back or ignore the WAL record."). This block is
  NOT attached to any function — line 370 is `// --- SplitCommit ---`, a
  different section's header, with no blank line between line 369 and 370. This
  is dead/orphaned documentation.

- Lines 370-512: the real `--- SplitCommit ---` section: `SplitCommitEntry`,
  `SplitCommitPayload`, `Encode`/`DecodeSplitCommitPayload`,
  `NewSplitCommitRecord`, `AsSplitCommit`. Untouched.

- **Lines 514-541: the real, correctly-placed `// --- fsync-before-apply write
  path ---` header + `AppendAndApply` doc comment**, immediately preceding
  `func AppendAndApply(...)` at line 542 (through line 557). This is the actual
  function and its doc comment — content is byte-for-byte identical to the
  orphaned block at 342-369 (confirming it's a copy-paste leftover, presumably
  from when the SplitCommit section was inserted between an original
  AppendAndApply doc comment placement and the function itself during task
  2b.3.6), but this copy is correctly attached to the function and must NOT be
  touched.

**Discovery conclusion:** Delete lines 342-369 inclusive (the orphaned header +
doc comment), leaving line 370's `// --- SplitCommit ---` as the first line of
that section (matching the existing header-then-blank-then-doc pattern used by
every other section in this file, e.g. line 141 `// --- CatalogPut ---`).

No other file in `engine/wal/` needs changes for this subtask. No behavioral code
paths are touched — this is a pure comment deletion.

**Post-implementation confirmation:** `grep -n "fsync-before-apply write path"
engine/wal/record.go` after the edit returns exactly one match (the real header,
now at line 486, immediately preceding the real doc comment and
`func AppendAndApply` at line 514), confirming the orphaned duplicate was
removed and the real one is intact.
