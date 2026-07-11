# Architecture discovery — subtask 4.5.3.4

## Order followed
1. `.cdr/index/*.jsonl` — no `file.jsonl` entry for engine/split/execute.go;
   `feature.jsonl` has no split-execute entries (grepped); `task.jsonl` has no
   4.5.3.4 entry yet (this is the first run for it). `regression.jsonl` not
   directly relevant (checked, no normalization-related entries).
2. `.cdr/memory/pending.md` — the exact debt entry this subtask closes (see
   requirement.md). Confirms origin: task-2b.3.3 (issue #12), finding surfaced
   in `.cdr/runs/2026-07-07/020-verification/`.
3. `docs/LLD/split.md` — status: "scaffold only" (stale relative to the real,
   fully-implemented `engine/split/execute.go`; last_synced_commit predates
   most of Phase 2b/4.5 work). Does not mention B+Tree key format at all, so
   no doc contradicts adding a normalization layer; no LLD update required by
   this subtask (issue's "Impacted modules" list does not include
   docs/LLD/split.md).
4. Touched files (read directly, since no LLD covers this): `engine/split/execute.go`
   in full (1130 lines), relevant sections of `engine/split/execute_test.go`
   (`TestExecuteSplitBtreeInsert`, ~line 442-575).
5. `engine/btree/insert.go`, `engine/btree/lookup.go` — `btree.Tree.Insert(path
   string, fileID uint64) error` and `btree.Tree.Lookup(path string) (fileID
   uint64, found bool, err error)`. Both treat `path` as an opaque ordered-key
   string (memcmp/`strings.Compare`-style ordering per node.go's key search);
   btree itself does zero interpretation of path structure. No existing
   normalization anywhere in `engine/btree` or elsewhere in the repo
   (`grep -rn "path.Clean\|filepath.Clean"` across engine/ returned nothing).

## Current raw-path-as-key call sites in engine/split/execute.go
- `ExecuteSplitBtreeInsert` (lines ~398-445): the subtask's named target.
  Inserts every `newPath` from `newPathFileIDs` map, then repoints `oldPath`.
- `ExecuteSplitAtomic`'s apply closure (lines ~962-969): same two-step
  pattern, inlined rather than calling `ExecuteSplitBtreeInsert` (this is the
  actual production commit path used by real split execution).
- `RecoverSplitCommits`'s replay loop (lines ~1111-1116): same two-step
  pattern again, inlined a third time, for crash recovery.

All three sites are within `engine/split/execute.go`, the single file the
issue names in "Impacted modules". No call site lives in `engine/btree`,
`engine/catalog`, or any other package — this is entirely a
`engine/split`-owned concern, consistent with the scope restriction for this
run (execute.go + execute_test.go only).

## Design decision
Introduce one unexported helper, `normalizeTopicPath(path string) string`, in
execute.go, and route all three raw `tree.Insert(rawPath, ...)` call sites
(plus `oldPath`) through it before using the result as the B+Tree key. This:
- Gives ExecuteSplitBtreeInsert a "defined normalization/canonicalization
  layer" per the acceptance criteria's literal wording.
- Keeps ExecuteSplitAtomic and RecoverSplitCommits self-consistent with
  ExecuteSplitBtreeInsert (same canonical key for the same logical path),
  avoiding a latent bug where the crash-recovery replay path or the real
  atomic-commit path could diverge from the standalone primitive.
- Is additive/backward compatible: canonicalization is idempotent
  (`normalizeTopicPath(normalizeTopicPath(p)) == normalizeTopicPath(p)`), and
  for already-canonical paths (the common case in existing fixtures, e.g.
  `"new/part-1.md"`, `"fixture-original.md"`) it is a no-op, so no existing
  test's expected Lookup behavior changes.

## Normalization rules chosen (documented in the function's doc comment)
- Convert backslashes to forward slashes (Windows-style separators typed by a
  caller normalize to the same key as forward-slash form).
- Collapse repeated slashes (`a//b` -> `a/b`).
- Strip leading `./` segments and one or more trailing slashes (`a/b/` ->
  `a/b`), matching the test spec's explicit "trailing separators" example.
- Case is deliberately left untouched (topic paths are treated as
  case-sensitive, matching Markdown filename conventions used throughout
  fixtures/tests; no requirement text calls for case-folding, and folding case
  would be a much bigger, separately-risky semantic change).
- Empty-after-normalization is rejected as an error at call sites that
  already validate non-empty paths (ExecuteSplitBtreeInsert already errors on
  `newPath == ""`/`oldPath == ""` before normalization runs, so this is
  belt-and-suspenders, not new user-facing behavior).

## Dependents / blast radius check
- `ExecuteSplitBtreeInsert`, `ExecuteSplitAtomic`, `RecoverSplitCommits` are
  the only production callers of `tree.Insert` for topic paths in the whole
  repo (confirmed via repo-wide grep, consistent with 2b.3.3's own doc
  comment claiming the same). No other package calls these three functions
  with paths that assume the OLD raw-key behavior in a way normalization
  would break: fixtures always use canonical-looking paths already
  (`"fixture-original.md"`, `"new/part-1.md"`, `"a.md"`, `"b.md"`), so
  `normalizeTopicPath` is a no-op on 100% of pre-existing test inputs.
- `guard.go`/`orchestrate.go`/`content.go` (catalog) are NOT touched by this
  change and do not call `tree.Insert` directly for topic paths (confirmed by
  grep); no risk of cross-file coupling outside the two files in scope.
