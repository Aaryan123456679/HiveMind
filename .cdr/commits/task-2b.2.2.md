# task-2b.2.2 — Deterministic mock SplitProposer (subtask 2 of 2, issue #11)

## Summary
Second and last subtask under GitHub issue #11 ("[2b] SplitProposer abstraction + deterministic mock (engine/split/)", Epic Phase 2b: Auto-split). Adds `engine/split/proposer_mock.go` (`MockSplitProposer`) and `proposer_mock_test.go`: a test-only, caller-configurable, deterministic implementation of 2b.2.1's `SplitProposer` interface, returning fixed `[{newPath, sectionRanges}]` + redirect-summary fixtures so split-sequence testing (issue #12) is unblocked before the real gRPC-backed `ProposeSplit` RPC exists. Implementation landed as `3c83d72`; a follow-up handoff.json commit `f8abd00` completed the run's bookkeeping. Issue #11 is now fully closable (both subtasks verified).

## Features
- `MockSplitProposer` struct: keyed `Plans map[string]SplitPlan` and `Errs map[string]error`, both indexed by exact `fileContent` (as a string), letting a test register per-input fixtures without any split-decision logic of its own.
- `WithPlan(fileContent, SplitPlan)` / `WithErr(fileContent, error)` fluent builders for registering per-input fixtures on a `*MockSplitProposer`.
- `DefaultPlan` / `DefaultErr` fallback fields plus `NewMockSplitProposer(defaultPlan)` constructor, covering the common "one fixed plan regardless of input" test case called out in the issue's acceptance criteria.
- Documented precedence rule: if a `fileContent` key is present in both `Errs` and `Plans`, `Errs` wins and `ProposeSplit` returns a zero `SplitPlan`.
- `FixtureSplitPlan` / `FixtureFileContent`: a ready-made, well-formed `SplitPlan` (two `SplitFileProposal`s tiling a 24-byte fixture via non-overlapping `SectionRange`s) plus its paired input, satisfying 2b.2.1's carried-forward `SectionRange` bounds invariant (`0 <= Start <= End <= len(fileContent)`) by construction.

## Impact
Non-blocking comments carried forward from verification (both addressed directly in this commit as trivial doc-only changes, since they were assessed as quick fixes rather than requiring a separate pending.md follow-up):
- The `Errs`-over-`Plans` precedence rule was previously documented only on the `Errs` field; a cross-reference has been added directly to the `ProposeSplit` method's own doc comment for discoverability at the call site.
- The `Plans`/`Errs` maps now carry an explicit configure-once/read-only concurrency-safety doc comment, so future reuse (e.g. issue #12's split-sequence tests) knows the maps are safe for concurrent reads only after configuration is complete, with no synchronization for concurrent writes.

Also noted: the `3c83d72` + `f8abd00` commit-split (implementation, then a follow-up handoff.json commit) was a minor process slip in the implementation run, not a correctness issue — no action required.

No regression: `engine/split/proposer.go` (2b.2.1) and all of issue #10's files (`trigger.go`, `guard.go`, `orchestrate.go`) are untouched.

## Verification
- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: 2026-07-07-013-verification
- **Details**: All dimensions PASS except `maintainability` (PASS_WITH_COMMENTS, the two doc-comment gaps above). Zero must-fix findings, confidence HIGH. Commits reviewed: `3c83d72`, `f8abd00`.

## Release Notes
`engine/split` gains `MockSplitProposer`, a deterministic, caller-configurable test double for the `SplitProposer` interface (2b.2.1), plus ready-made `FixtureSplitPlan`/`FixtureFileContent` fixtures — unblocking split-sequence testing (issue #12) ahead of the real gRPC `ProposeSplit` RPC. This closes out issue #11: the `SplitProposer` abstraction + deterministic mock feature is complete. No breaking API change; new test-support surface only, not itself wired into any live append or split-execution path. Doc-comment clarifications (precedence rule, map concurrency contract) added post-verification, no behavior change.
