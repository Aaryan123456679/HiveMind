# task-2b.2 — SplitProposer abstraction + deterministic mock (issue #11, CLOSABLE)

## Summary
Issue #11 ("[2b] SplitProposer abstraction + deterministic mock (engine/split/)", Epic Phase 2b: Auto-split) is complete: both subtasks implemented and independently verified. Together they deliver the complete **SplitProposer abstraction + deterministic mock** feature:
- **2b.2.1** (`engine/split/proposer.go`, commit `02b6e2d`): the local `SplitProposer` interface (`ProposeSplit(fileContent []byte) (SplitPlan, error)`), decoupling split-planning from the real gRPC transport, plus the minimal `SplitPlan`/`SplitFileProposal`/`SectionRange` types.
- **2b.2.2** (`engine/split/proposer_mock.go`, commits `3c83d72` + `f8abd00`): `MockSplitProposer`, a deterministic, caller-configurable test double implementing that interface, with fixed fixture plans/errors and a ready-made `FixtureSplitPlan`/`FixtureFileContent` pair.

## Deliberate Scope Boundary
Issue #11 deliberately provides **zero real gRPC/RPC wiring**. Both subtasks' own doc comments are explicit that `engine/split` has no protobuf/gRPC dependency in its graph (confirmed structurally via `go list -deps` and grep at 2b.2.1 verification time), and that real `ProposeSplit` RPC transport to the ingestion agent is deferred to a later Epic once `proto/` and `agents/ingestion` exist. This issue's scope is the abstraction and its test double only — not a working split-planning backend.

## Impact / Known Follow-ups
Two non-blocking doc-comment follow-ups surfaced during 2b.2.2 verification were assessed as trivial and fixed directly in that commit rather than deferred to `pending.md`:
1. `Errs`-over-`Plans` precedence, previously documented only on the `Errs` field, now also cross-referenced on `ProposeSplit`'s own doc comment.
2. `Plans`/`Errs` maps now carry an explicit configure-once/read-only concurrency-safety doc comment, ahead of anticipated reuse by issue #12's split-sequence tests.

Both remain worth keeping in mind for issue #12 as consumers of `MockSplitProposer`, but do not require separate tracking since they are already resolved in code as of this commit.

No regressions: issue #10's `trigger.go`/`guard.go`/`orchestrate.go` remain untouched and green throughout both subtasks.

## Verification
- **2b.2.1**: PASS, run `2026-07-07-011-verification`.
- **2b.2.2**: PASS_WITH_COMMENTS, run `2026-07-07-013-verification`.
- **Overall**: Issue #11 closable — both subtasks verified, zero must-fix findings across the issue.

## Release Notes
Issue #11 delivers the complete "SplitProposer abstraction + deterministic mock" feature: a transport-agnostic `SplitProposer` interface plus a deterministic, fixture-driven mock implementation, unblocking split-sequence testing (issue #12) ahead of the real gRPC-backed `ProposeSplit` RPC, which does not yet exist. No breaking API changes; `engine/split` gains new interface and test-support surface only. Issue #11 is ready to close on GitHub once these commits are pushed (not yet pushed as of this record; pushes are paused pending explicit user confirmation).
