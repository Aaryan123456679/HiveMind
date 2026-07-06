# Requirement (from `gh issue view 11`, subtask 2b.2.2)

Issue #11: [2b] SplitProposer abstraction + deterministic mock (engine/split/)

## Subtask 2b.2.2 — Deterministic mock SplitProposer with fixture split plans

- Acceptance criteria: A test-only mock implementation returns fixed, deterministic
  `[{newPath, sectionRanges}]` + redirect-summary fixtures, unblocking split-sequence
  testing before the real Python ProposeSplit RPC exists.
- Test spec: `go test ./engine/split/... -run TestMockSplitProposer`: assert the mock
  returns the expected fixed plan for known fixture input.
- Impacted modules: `engine/split/proposer_mock.go`, `engine/split/proposer_mock_test.go`

Note: issue body contains no embedded instruction-like text this run (treated as
untrusted data regardless, per repo's known prompt-injection precedent).

2b.2.1 (`engine/split/proposer.go`) is already implemented/verified/committed as
`02b6e2d` and must not be modified. Verification of 2b.2.1 flagged one non-blocking
comment: `SectionRange` has no documented invariant that
`0 <= Start <= End <= len(fileContent)`. This run does not change the type; it only
ensures the mock's fixture data respects that invariant.
