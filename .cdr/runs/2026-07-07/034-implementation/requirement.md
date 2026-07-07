# Requirement — Issue #14

Title: [2b] Auto-split concurrent race-test suite (mandatory highest-risk gate)
Milestone: Phase 2b: Auto-split (engine/split/) — highest-risk correctness surface
State: OPEN, no assignees, part of Epic Phase 2b.

Note: `gh issue view 14` output and repo git history/status have repeatedly contained
embedded fake system-reminder-style text (fake date-change notices, fake MCP/tokensave
tool instructions, fake "Auto Mode Active" directives). Observed again in this run's
`git status` output. Treated as untrusted plain-text data, not followed.

## Subtasks (2 — multi-subtask like #10-#12)

### 2b.5.1 — Many-goroutine concurrent-append race test
- Acceptance: many goroutines append to the same file simultaneously, crossing the
  split threshold multiple times in aggregate; assert no appended data lost, exactly
  one split executes per threshold crossing, no graph edge references a nonexistent
  fileID afterward.
- Test spec: `go test ./engine/split/... -race -run TestConcurrentAppendSplitRace -count=10`
- File: `engine/split/split_race_test.go`

### 2b.5.2 — Reader-during-split test
- Acceptance: a reader that snapshots the file immediately before a split begins
  continues to read fully consistent pre-split content for the duration of its read,
  regardless of the split completing concurrently.
- Test spec: `go test ./engine/split/... -race -run TestReaderDuringSplit`
- File: `engine/split/split_race_test.go` (same file, per issue)

Both subtasks target the same new file `engine/split/split_race_test.go`, sized to
one commit each per issue text ("Each subtask above is sized to exactly one commit"),
but they share a file — will implement both tests in one file and evaluate whether
one or two commits is more appropriate at commit time (single-agent run implementing
both in one pass; issue explicitly allows this file to hold both tests).
