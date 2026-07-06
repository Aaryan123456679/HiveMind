# Architecture Discovery

## `engine/split/proposer.go` (read in full, not modified)

- `SplitProposer` interface: `ProposeSplit(fileContent []byte) (SplitPlan, error)`.
  Must not mutate `fileContent`; non-nil error means the returned `SplitPlan` must not
  be acted on.
- `SplitPlan{ Files []SplitFileProposal; RedirectSummary string }`.
- `SplitFileProposal{ NewPath string; SectionRanges []SectionRange }`.
- `SectionRange{ Start, End int }` — half-open `[Start, End)` byte-offset range into the
  original `fileContent`, per doc comment. No compiler-enforced invariant; mock fixtures
  must self-respect `0 <= Start <= End <= len(fileContent)`.
- Doc comments explicitly point at 2b.2.2 (this subtask) as the deterministic
  fixture-backed mock, and explicitly scope out fileID allocation / B+Tree repointing /
  graph edges to issue #12.

## Existing mock/fake patterns in repo

- No `*_mock.go` files exist anywhere in the repo prior to this change (`find . -iname
  "*mock*"` returned nothing under `engine/`).
- `engine/split/proposer_test.go` already contains a trivial `fakeSplitProposer`
  (lowercase, `_test.go`-local, single hardcoded plan) used only to assert the interface
  contract in `TestSplitProposerInterface`. Its doc comment explicitly says it "is not
  the deterministic fixture mock added by 2b.2.2 (engine/split/proposer_mock.go)" —
  i.e. 2b.2.1's author already anticipated this subtask's file name and scope.
- Decision: since 2b.2.2's mock is meant to be reusable by later subtasks/issue #12
  test-writing (per task framing), it is placed in a plain non-`_test.go` file
  (`proposer_mock.go`) exporting `MockSplitProposer`, not a `_test.go`-only helper.
  This matches Go idiom for "testutil"-style exported mocks meant for cross-package
  reuse, and matches the existing doc-comment's own naming expectation.

## Design decision: caller-configurable vs. single hardcoded plan

Per task framing, a caller-configurable mock (keyed fixture plans + a default) is more
useful for "unblocking split-sequence testing" than one single hardcoded plan, since
different tests will want different fixture inputs/outputs. Implemented
`MockSplitProposer` with:
- `Plans map[string]SplitPlan` / `Errs map[string]error` keyed by exact `fileContent`
  (as `string(fileContent)`), for tests wanting input-specific fixtures.
- `DefaultPlan` / `DefaultErr` fallback for tests that just want one fixed plan
  regardless of input (still fully deterministic).
- `WithPlan` / `WithErr` chainable builder methods and a `NewMockSplitProposer`
  constructor for ergonomics.
- Package-level `FixtureSplitPlan` + `FixtureFileContent` canned fixtures usable
  directly without constructing custom data.
