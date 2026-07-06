# task-2b.2.1 — SplitProposer interface (subtask 1 of 2, issue #11)

## Summary
First of two subtasks under GitHub issue #11 ("[2b] SplitProposer abstraction + deterministic mock (engine/split/)", Epic Phase 2b: Auto-split). Adds `engine/split/proposer.go`: a local `SplitProposer` interface (`ProposeSplit(fileContent []byte) (SplitPlan, error)`) that decouples split-planning logic from the real `ProposeSplit` RPC transport. `engine/split/` now depends only on this interface — no gRPC/agent-transport dependency exists yet, by design; real gRPC wiring is deferred to a later epic once `proto/` and `agents/ingestion` exist.

## Features
- `engine/split.SplitProposer`: single-method interface, `ProposeSplit(fileContent []byte) (SplitPlan, error)`, documented as must-not-mutate-input and stateless/proposal-only.
- `SplitPlan` / `SplitFileProposal` / `SectionRange` types: minimal shape carrying only what issue #12's real implementation and 2b.2.2's deterministic mock will need — no fileID allocation, no content/stub writes, no redirect bookkeeping (that scope stays with issue #12's execution layer, per `orchestrate.go`'s existing documented boundary).
- Zero gRPC/protobuf coupling anywhere in the package's dependency graph — verified structurally (`go list -deps`, grep across `split/`), not just by convention.
- Doc comments explicitly cross-reference the sibling subtask (2b.2.2, deterministic mock) and issue #12 (real execution), keeping scope boundaries discoverable in-code.

## Impact
- This is **subtask 1 of 2** under issue #11. Sibling subtask **2b.2.2** (deterministic mock `SplitProposer` returning fixed `[{newPath, sectionRanges}]` + redirect-summary fixtures in `proposer_mock.go`/`proposer_mock_test.go`) remains open; issue #11 is not yet closable.
- No regression: `trigger.go`, `guard.go`, `orchestrate.go` (issue #10, 2b.1.1–2b.1.3) are untouched — confirmed byte-identical via `git diff` against the prior verified commit. Full `go test ./split/... -race` green, including all prior subtask tests alongside the new `TestSplitProposerInterface`.
- **Non-blocking comment carried forward**: `SectionRange` has no documented invariant that `0 <= Start <= End <= len(fileContent)`. Acceptable for an interface-only subtask (no validation logic exists to enforce it yet), but worth addressing when 2b.2.2's mock and issue #12's real consumer start constructing/consuming `SectionRange` values, so both share the same validation expectation from the start.

## Verification
- **Verdict**: PASS
- **Run ID**: 2026-07-07-011-verification
- **Details**: All 9 dimensions passed (8 `pass`, 1 `pass_with_comment` on the `SectionRange` bounds-invariant gap above). Zero must-fix findings. Confidence high: `go build ./...`, `go vet ./...`, `gofmt -l .` all clean; `go list -deps ./split/...` plus grep across `split/` confirm zero gRPC/protobuf imports; `TestSplitProposerInterface` genuinely exercises the interface contract (assigns the fake to a `SplitProposer`-typed variable, calls through the interface value, covers both success and error paths). Scope check confirmed only `proposer.go`/`proposer_test.go` touched — no changes to `trigger.go`/`guard.go`/`orchestrate.go`. Commit reviewed: `02b6e2d`.

## Release Notes
`engine/split` gains a `SplitProposer` interface (`ProposeSplit(fileContent) -> SplitPlan`) decoupling split-planning from the real gRPC transport, which doesn't exist yet. Minimal `SplitPlan`/`SplitFileProposal`/`SectionRange` types only — no over-design ahead of the consumers that will need them (2b.2.2's mock, issue #12's real execution). No breaking API change; new package surface only. Known, tracked follow-up: `SectionRange`'s bounds invariant (`0 <= Start <= End <= len(fileContent)`) is not yet documented/enforced — to be addressed as 2b.2.2 and issue #12 begin consuming it.
