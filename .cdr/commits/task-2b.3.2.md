# task-2b.3.2 — Split execution: write redirect stub + transition to StatusRedirect (subtask 2/6, issue #12)

## Summary

Second of 6 subtasks under GitHub issue #12 ("[2b] Atomic split-transaction execution",
Epic Phase 2b: Auto-split). Adds `ExecuteSplitRedirectStub` to `engine/split/execute.go`,
consuming 2b.3.1's `ExecuteSplitAllocateAndWrite` output (`map[string]uint64`, NewPath ->
new fileID) to perform the next step in the split sequence: replace the original file's
content with a deterministic redirect stub and transition its catalog record from
`StatusSplit` to `StatusRedirect` with `RedirectTargetIDs` populated.

## Features

- `ExecuteSplitRedirectStub`: durable `StatusSplit -> StatusRedirect` catalog transition,
  refusing via `ErrNotSplit` if the original record is not already in `StatusSplit` --
  step two of the two-step transition whose step one (`StatusSplitting -> StatusSplit`)
  is 2b.1.3's existing `Orchestrator.EndSplit`.
- Deterministic redirect-stub content write: overwrites the original fileID's content
  file with a fixed "HIVEMIND-REDIRECT-STUB v1" marker, reusing the same fileID rather
  than allocating a new one -- `RedirectTargetIDs` on the catalog record remains the
  actual source of truth for readers, the stub content is a marker only.
- Reuses the crash-safe temp-file-same-dir -> Write -> Sync -> Close -> Rename
  write-then-WAL-append-before-catalog-apply pattern established in 2b.3.1 and
  `catalog.ContentStore`, keeping the durability story consistent across the split
  execution primitives.

## Impact

- Subtask 2 of 6 under issue #12; issue remains open pending 2b.3.3-2b.3.6 (topic-path
  handling, graph append-only edges, inbound-edge re-pointing, final WAL-covered atomic
  transaction wrapper + writer-queue release).
- Non-blocking comments carried forward from verification (not required before merge,
  tracked follow-up alongside remaining 2b.3.x work):
  1. The `wrong_status` and `record_not_found` subtests in `TestSplitRedirectStub`
     assert only the returned error, not explicit zero-side-effects (i.e. they don't
     independently verify the stub file is absent/unchanged and the catalog record is
     byte-identical to before the call).
  2. The "check Status before trusting content bytes" invariant for a reused fileID is
     documented only in doc comments, not enforced by any code-level assertion helper.
  3. Stub-content determinism depends on the caller supplying `newFileIDs` in a stable
     order -- 2b.3.6 should pin a canonical ordering contract when wiring 2b.3.1's
     `ExecuteSplitAllocateAndWrite` map output into this function, to avoid a latent
     non-determinism bug at integration time. Tracked in `.cdr/memory/pending.md` for
     2b.3.6 to consume.
  4. The non-atomic window between the stub content write and the catalog status
     transition is honestly disclosed (not hidden) and recoverable; acceptable given
     2b.3.6 owns end-to-end atomicity for the whole split transaction.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: 2026-07-07-018-verification
- Commit reviewed: `73279a0`
- Build/vet/gofmt clean. `go test ./split/... -run TestSplitRedirectStub -count=5`: 5/5
  runs PASS, 6/6 subtests each. Full `./split/... -race -v -count=1` split suite PASS,
  zero regressions. `./catalog/... -count=1` regression suite PASS.

## Release Notes

- `engine/split`: added `ExecuteSplitRedirectStub`, transitioning a split's original
  catalog record `StatusSplit -> StatusRedirect` and replacing its content with a
  deterministic redirect stub. No breaking API change; extends the split-execution
  package surface started in 2b.3.1.
