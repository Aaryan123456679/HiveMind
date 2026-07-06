# task-2b.3.1 — Split execution: allocate fileIDs + write split-off content files (subtask 1/6, issue #12)

## Summary
First of 6 subtasks under GitHub issue #12 ("[2b] Atomic split-transaction execution", Epic Phase 2b: Auto-split — highest-risk correctness surface). Adds `engine/split/execute.go`: `ExecuteSplitAllocateAndWrite` takes a validated `SplitPlan` (produced by 2b.2's `SplitProposer`), allocates exactly one new fileID per `SplitFileProposal`, and durably writes the corresponding content file containing the exact concatenated bytes of that proposal's declared `SectionRanges`. This subtask deliberately does not touch the catalog, B+Tree, or graph, and does not provide cross-step atomicity — that is issue #12's final subtask (2b.3.6) via WAL-wrapped transaction. 2b.3.1 provides only: (1) fileID allocation via the existing, shared allocator convention, and (2) crash-safe writes of new content files in isolation.

## Features
- `ExecuteSplitAllocateAndWrite`: validates the incoming `SplitPlan` (rejects empty/duplicate `NewPath`, empty plans, nil `IDAllocator`/`ContentStore` dependencies, and malformed `SectionRange`s — zero-length, out-of-bounds, inverted, and overlapping ranges all rejected with explicit errors) before performing any allocation or write.
- fileID allocation reuses the real, shared `catalog.IDAllocator.Next()` (the same durable, fsync-before-return, monotonic-counter instance used for real catalog records) rather than a separate ID space, so a later 2b.3.2 `catalog.Put` for these fileIDs cannot collide.
- New content files are written via a temp-file-same-dir → Write → Sync → Close → Rename → cleanup-on-each-error-path sequence that mirrors `catalog.ContentStore.writeContentFile` byte-for-byte, using `cs.ContentPath(newFileID)` for the same `content/<fileID>.v1.md` convention.
- `SectionRange`s are assembled in the proposal's declared order (not sorted order), preserving author intent for reordered splits.

## Impact
- Subtask 1 of 6 under issue #12; issue remains open pending 2b.3.2–2b.3.6 (redirect/status writer, topic-path handling, graph append-only edges, inbound-edge re-pointing, and the final WAL-covered atomic transaction wrapper + writer-queue release).
- Establishes the fileID-allocation and durable-write primitive that all remaining 2b.3.x subtasks build on; verified structurally distinct from (but conventionally identical to) `catalog.ContentStore`'s write path, so no drift risk once 2b.3.2 wires these fileIDs into real catalog records.
- Non-blocking comments carried forward (not required before merge, tracked for follow-up alongside remaining 2b.3.x work):
  - No test exercises a zero-length range directly adjacent to (touching the boundary of) another non-zero range across two separate proposals — the overlap-guard's exclusion logic for zero-length ranges at shared boundaries has not been hand-traced/asserted in this form.
  - `extractSections` (the section-range-to-bytes assembly helper) has no defensive re-check of range validity if it were ever reused outside the validated `ExecuteSplitAllocateAndWrite` call path; it currently relies entirely on upstream validation.

## Verification
- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: 2026-07-07-016-verification
- Commits reviewed: `01411d8` (feat: allocate new fileIDs and write split-off content files), `a7c3c74` (chore: add metadata.json/handoff.json for the implementation run).
- All dimensions (requirements, architecture conformance, scope discipline, regression risk, test coverage, security, maintainability) passed; security and the two items above were flagged `pass_with_comment`/non-blocking only.
- Regression suite: `go test ./split/... -race -v -count=1` (all subpackages, prior 2b.1.x/2b.2.x included) green; `go test ./catalog/... -count=1` green (no regressions); `go test ./split/... -run TestSplitAllocateAndWrite -count=5` green (no flakiness).
- All 9 claimed subtests confirmed non-vacuous on direct read: fixture_plan, multi_range_single_file, empty_range, out_of_bounds_range, inverted_range, overlapping_ranges, duplicate_new_path, empty_plan, nil_deps.

## Release Notes
`engine/split` gains its first real split-execution primitive: `ExecuteSplitAllocateAndWrite` allocates new fileIDs through the existing catalog allocator and writes split-off content files with the same crash-safe write technique as the catalog's own content store. This is execution-primitive-only — no catalog records, B+Tree entries, or graph edges are created yet, and there is no cross-step atomicity (that lands with 2b.3.6). Two non-blocking follow-ups tracked for later in the 2b.3.x sequence: no test for a zero-length range touching another range's boundary across two proposals, and no defensive re-validation in the section-assembly helper if reused outside its current call path. No breaking API change; new package surface only.
