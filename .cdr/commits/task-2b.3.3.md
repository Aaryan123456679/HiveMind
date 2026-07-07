# task-2b.3.3 — Split execution: insert new topic paths into B+Tree, repoint old path (subtask 3/6, issue #12)

## Summary

Third of 6 subtasks under GitHub issue #12 ("[2b] Atomic split-transaction execution",
Epic Phase 2b: Auto-split). Adds `ExecuteSplitBtreeInsert` to `engine/split/execute.go`,
consuming 2b.3.1's `ExecuteSplitAllocateAndWrite` output (`map[string]uint64`, NewPath ->
new fileID) to make the split's new topic paths resolvable via the B+Tree and to
explicitly repoint the old path's tree entry to the original fileID reused by 2b.3.2's
redirect stub.

## Features

- `ExecuteSplitBtreeInsert(tree, oldPath, originalFileID, newPathFileIDs)`: inserts each
  new topic path into the given `*btree.Tree`, pointing it at its new fileID (new-path
  insertion), then explicitly repoints `oldPath` to `originalFileID`.
- Old-path repoint is a safe no-op by construction: per `btree.Tree.Insert`'s documented
  upsert semantics (traced in `engine/btree/insert.go` -- existing key updates its fileID
  in place, no structural tree change), and because 2b.3.2 reuses `originalFileID` for the
  redirect stub rather than allocating a new one, reissuing the insert for `oldPath`
  cannot corrupt or restructure the tree; it is issued explicitly for clarity/documentation
  of intent rather than out of structural necessity.

## Impact

- Subtask 3 of 6 under issue #12; issue remains open pending 2b.3.4-2b.3.6 (graph
  append-only edges, inbound-edge re-pointing, final WAL-covered atomic transaction
  wrapper + writer-queue release).
- Non-blocking comments carried forward from verification (not required before merge,
  tracked follow-up alongside remaining 2b.3.x work):
  1. Raw topic-path strings are used directly as B+Tree keys with no normalization or
     namespace layer. This may need reconciliation once a real topic-path indexing
     scheme is designed later. Tracked in `.cdr/memory/pending.md`.
  2. No subtest covers the case where `oldPath` is absent from the tree prior to the
     call, though code analysis shows this would behave correctly as a fresh insert
     (no assumption in the implementation requires pre-existence).

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: 2026-07-07-020-verification
- Commit reviewed: `ee2a658`
- Build/vet/gofmt clean. `go test ./split/... -run TestSplitBtreeRepoint -count=5`: 5/5
  runs PASS deterministically. Full `./split/... -race -v -count=1` split suite PASS,
  zero regressions.

## Release Notes

- `engine/split`: added `ExecuteSplitBtreeInsert`, making a split's new topic paths
  resolvable via the B+Tree and repointing the old path's entry to the reused original
  fileID. No breaking API change; extends the split-execution package surface started
  in 2b.3.1/2b.3.2.
