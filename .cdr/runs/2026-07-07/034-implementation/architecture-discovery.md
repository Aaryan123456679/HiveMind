# Architecture discovery — issue #14

## Sources consulted (index-first order)
1. `.cdr/index/task.jsonl` (task-2b.* entries) — full history of 2b.1-2b.4 subtasks.
2. `.cdr/memory/pending.md` — known-gaps ledger.
3. `docs/LLD/split.md` — "Known risks" section (source of this issue's framing).
4. Direct source reads: `engine/split/{trigger,guard,orchestrate,execute}.go`,
   `engine/catalog/content.go`, `engine/graph/edge_append.go`, `engine/wal/record.go`
   (SplitCommitPayload/Entry), plus each subtask's existing `*_test.go` for reusable
   test helpers.

## Real integration seam (confirmed by direct source read)

- `engine/split.Trigger` is NOT wired into `catalog.ContentStore.Append` (confirmed:
  `Append` computes its own local `thresholdCrossed` bool independently at
  content.go:391, matching pending.md's disclosed gap). `Append` is fully
  serialized per fileID via `cs.stripes[stripeFor(fileID)]`, so concurrent Appends
  to the SAME fileID cannot race on the read-modify-write, and `thresholdCrossed`
  fires deterministically exactly once per crossing (monotonic size growth).
- The achievable integration point for "many goroutines appending to the same
  file simultaneously, exactly one split per crossing" is: writer goroutines call
  `catalog.ContentStore.Append`; whichever call observes `thresholdCrossed==true`
  becomes responsible for driving `Orchestrator.BeginSplit` ->
  `ExecuteSplitAtomic` -> (guard release happens inside `ExecuteSplitAtomic`
  itself on success, or must be handled via `AbortSplit` on failure) manually.
  This mirrors the task brief's guidance exactly.
- `Orchestrator.AdmitWrite` is the write-admission gate a "well-behaved" writer
  must call before `Append` to respect the SPLITTING window (not wired into
  `Append` itself — callers must opt in). The race test's writer goroutines call
  `AdmitWrite` before every `Append`, back off and retry on `ErrSplitInProgress`,
  matching the documented "queued rather than applied" contract.
- `ContentStore.LockFileContent(fileID)` (added in issue #13's fix cycle) is the
  stripe-lock primitive `ExecuteSplitAtomic` uses internally for the redirect-stub
  write; not needed directly by the race test's harness code, but relevant to
  subtask 2b.5.2's reader-during-split test since `ReadPartial` takes the same
  lock.

## REAL BUG FOUND: split leaves new fileIDs with no catalog.CatalogRecord

Direct read of `ExecuteSplitAtomic` (execute.go, the atomic apply closure) and
`RecoverSplitCommits` (both the live path and the crash-replay path) confirms:
only `cat.Put(updated)` for `originalFileID` is ever called. No
`catalog.CatalogRecord` is ever created for any of the newly allocated fileIDs
produced by `ExecuteSplitAllocateAndWrite` — their content files are durably
written to disk, B+Tree entries point at them, and graph SPLIT_SIBLING/REDIRECT
edges reference them, but `cat.Get(newFileID)` returns `ErrNotFound` forever
afterward.

Since `catalog.ContentStore.Read` and `.Append` both call `cs.cat.Get(fileID)`
first and fail with `ErrNotFound` if it does not exist (content.go:290-296,
336-347), this means **every split-off file is permanently unreadable and
unappendable through the normal ContentStore API** — a genuine data-loss-shaped
bug (content exists on disk and is graph/B+Tree-referenced, but is inaccessible
through the only supported read/write path), and is exactly the kind of gap this
race test's "no data loss" assertion is designed to catch: any test that reads
back post-split content via `cs.Read(newFileID)` fails immediately, pre-empting
any actual race-condition testing.

No existing test (`TestSplitAtomicCommit`, `assertFullSplitApplied`, or any
2b.3.x subtask test) asserts `cat.Get(newFileID)` succeeds; this was never
covered. Not previously disclosed in `.cdr/memory/pending.md`.

### Root cause
`ExecuteSplitAtomic`'s single `wal.AppendAndApply` closure and
`RecoverSplitCommits`'s replay closure both build B+Tree/graph state from
`wal.SplitCommitPayload.Entries` (`{NewPath, FileID}` pairs) but never
reconstruct a `catalog.CatalogRecord` for each entry's FileID. `SplitCommitEntry`
itself carries no size information, so even if the omission were noticed, the
recovery path could not reconstruct a `CatalogRecord.SizeBytes` without re-reading
the content file (available in the live path but not necessarily meaningful to
rely on during replay, when content may pre-date this run's fsync guarantees).

### Fix
1. `engine/wal/record.go`: add `SizeBytes uint64` to `SplitCommitEntry`; extend
   `Encode`/`Decode` for the new 8-byte field per entry.
2. `engine/split/execute.go` (`ExecuteSplitAtomic`): compute each new path's
   content length from `plan.Files`/`extractSections` when building
   `wal.SplitCommitEntry` values; inside the apply closure, after
   `cat.Put(updated)` for `originalFileID`, additionally `cat.Put` a fresh
   `catalog.CatalogRecord{FileID: entry.FileID, CurrentVersion: 0,
   SizeBytes: entry.SizeBytes, Status: catalog.StatusActive}` for every entry.
3. `engine/split/execute.go` (`RecoverSplitCommits`): mirror the same new-record
   `cat.Put` loop from `payload.Entries` (now carrying `SizeBytes`), so crash
   replay produces the identical end state as the live path (idempotent, matches
   existing `cat.Put`-is-upsert convention).
4. `engine/wal/record_test.go`: extend the existing `SplitCommit` round-trip
   subtest's `want`/comparison to include non-zero `SizeBytes` per entry.
5. `engine/split/execute_test.go` (`assertFullSplitApplied`): add an assertion
   that `cat.Get(newFileID)` succeeds with `Status == catalog.StatusActive` and
   the expected `SizeBytes`, giving both `TestSplitAtomicCommit`'s live-path
   subtests and its 4 crash-injection/recovery subtests regression coverage for
   this fix.

This fix is strictly additive (new field appended after existing ones in the
wire format; new records only, no existing records mutated) and does not change
`ExecuteSplitAtomic`'s existing WAL-covered atomicity/idempotency guarantees.
