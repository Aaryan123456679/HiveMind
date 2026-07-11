# Plan — subtask 4.5.3.4

1. In `engine/split/execute.go`, add an unexported helper:
   `func normalizeTopicPath(path string) string` — converts backslashes to
   forward slashes, collapses repeated slashes, strips a leading `./`, and
   strips trailing slashes. Document its contract (idempotent, case-preserving,
   what it does NOT do) directly above it, next to the existing
   `ExecuteSplitBtreeInsert` doc comment cluster.

2. Update `ExecuteSplitBtreeInsert` (lines ~398-445):
   - After the existing empty-string validation (validation stays on the RAW
     input, so callers get the same "empty path" errors as before), normalize
     `oldPath` and every `newPath` before calling `tree.Insert`.
   - Keep the `newPath == oldPath` cross-check working on normalized forms too
     (so `"a/"` vs `"a"` are correctly caught as equal once normalized).

3. Update `ExecuteSplitAtomic`'s apply closure (lines ~962-969) to normalize
   `newPath` (from `newPaths`) and `oldPath` the same way before
   `tree.Insert`, so the real commit path stays consistent with
   `ExecuteSplitBtreeInsert`.

4. Update `RecoverSplitCommits`'s replay loop (lines ~1111-1116) identically,
   so crash-recovery replay produces the same canonical keys a live commit
   would have produced.

5. Add `TestSplitBtreeKeyNormalization` to `engine/split/execute_test.go`:
   - Seed a tree with `oldPath` under one raw form.
   - Call `ExecuteSplitBtreeInsert` with `newPathFileIDs` keyed by
     differently-formatted-but-equivalent path strings (e.g. trailing slash,
     backslash, double slash, leading `./`) alongside an equivalently
     differently-formatted `oldPath`.
   - Assert via `tree.Lookup` using the CANONICAL form that each resolves to
     the expected fileID, and that looking up the raw differently-formatted
     form ALSO resolves (since Lookup itself does not normalize — only
     insertion does — so this test also documents that callers must look up
     via the canonical form, or normalize their own Lookup calls; add an
     explicit sub-check clarifying this via a comment, since the acceptance
     criteria only requires insertion-side normalization, not adding
     normalization to btree.Tree.Lookup which is out of scope/another
     package).

6. Run `go test ./engine/split/... -race` (package-scoped) to confirm no
   regression, plus targeted `-run TestSplitBtreeKeyNormalization` and
   `-run TestExecuteSplitBtreeInsert` and `-run TestSplitAtomicCommit` and
   `-run TestRecoverSplitCommits` (or equivalent existing test names) for
   focused confirmation.

7. Self-consistency check, one commit (execute.go + execute_test.go +
   run dir only), handoff.
