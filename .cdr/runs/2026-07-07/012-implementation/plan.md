# Plan

1. Add `engine/split/proposer_mock.go`:
   - `MockSplitProposer` struct implementing `SplitProposer`, with `Plans`/`Errs` maps
     keyed by exact `fileContent` (string-keyed), plus `DefaultPlan`/`DefaultErr`
     fallback.
   - `NewMockSplitProposer(defaultPlan SplitPlan) *MockSplitProposer` constructor.
   - `WithPlan`/`WithErr` chainable builder methods.
   - Package-level `FixtureSplitPlan` (well-formed `SectionRange`s respecting
     `0 <= Start <= End <= len(fileContent)`) and paired `FixtureFileContent` (24 bytes,
     exactly tiled by the fixture's ranges).
2. Add `engine/split/proposer_mock_test.go` with `TestMockSplitProposer` covering:
   - default-plan fixed/deterministic return for known fixture input (repeat call ->
     identical result).
   - caller-registered plan keyed by exact fileContent, with fallback to default for
     unregistered input.
   - caller-registered error taking precedence over a registered plan for the same key.
   - default error path when no fixture matches.
   - explicit invariant check that fixture `SectionRange`s stay within
     `[0, len(FixtureFileContent)]`.
3. Do not touch `proposer.go`, `trigger.go`, `guard.go`, `orchestrate.go`.
4. Run self-consistency (build/vet/fmt, targeted test, full package race test) before
   committing.
