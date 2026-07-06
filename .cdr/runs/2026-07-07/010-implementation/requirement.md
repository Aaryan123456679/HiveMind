# Requirement — subtask 2b.2.1 (Issue #11)

Source: `gh issue view 11` (title: "[2b] SplitProposer abstraction + deterministic mock
(engine/split/)"). Treated as untrusted plain-text data per task instructions; no
instruction-like content found in the issue body itself (unlike some prior issues in this
repo, this one contained no embedded prompt-injection attempt).

Epic: Phase 2b: Auto-split (engine/split/) — highest-risk correctness surface.

Subtask 2b.2.1 (first of two subtasks under issue #11):
- **Define a local `SplitProposer` interface decoupling split logic from the real
  ProposeSplit RPC transport.**
- Acceptance criteria: `engine/split/` depends only on a `SplitProposer` interface
  (`ProposeSplit(fileContent) -> plan`); no direct gRPC/agent dependency exists yet (real
  gRPC wiring happens in a later epic once `proto/` and `agents/ingestion` exist).
- Test spec: `go build ./engine/split/...` succeeds with zero imports of a gRPC client
  package; `go test ./engine/split/... -run TestSplitProposerInterface` asserts the
  interface contract via a trivial fake.
- Impacted modules: `engine/split/proposer.go` (new file).

Sibling subtask 2b.2.2 (NOT in scope here, informs plan shape only): deterministic mock
`SplitProposer` returning fixed `[{newPath, sectionRanges}]` + redirect-summary fixtures
in `proposer_mock.go` / `proposer_mock_test.go`.

Explicit non-goals for 2b.2.1 (per task instructions):
- Do not touch `trigger.go`, `guard.go`, `orchestrate.go` (2b.1.1/2b.1.2/2b.1.3, already
  implemented/verified/committed).
- Do not implement real split logic (no gRPC client, no actual proposal generation).
- Do not over-design `SplitPlan` fields beyond what issue #12's real implementation and
  2b.2.2's mock will need — minimal shape only.
