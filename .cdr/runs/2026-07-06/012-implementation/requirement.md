# Requirement — task-2a.4.5

Final subtask of task-2a.4 (B-tree latch-crabbing concurrency, GitHub issue
#9). Closing it closes out ALL of Epic Phase 2a.

**Acceptance criteria**: a full concurrent mixed insert/delete/lookup
workload, across disjoint AND overlapping subtrees, whose final tree state
matches a deterministic oracle, with zero `-race` reports.

**Test spec**: `go test ./engine/btree/... -race -run TestConcurrentMixedWorkload -count=5`.

**Impacted module**: `engine/btree/btree_test.go` — NOTE: this file already
exists (from task-1.2.6, `TestPersistReload`/`TestLoadRootFreshIndexFile`);
this subtask APPENDS a new test to it rather than creating/overwriting it.

**Scope**: test-only capstone. No production code changes. Exercises
`Tree.Insert` (2a.4.2), `Tree.Delete` (2a.4.3), `Tree.Lookup` (2a.4.4)
together against one shared `*Tree`, at real scale, hunting for anything
genuine 3-way interleaving could surface that the per-operation suites
(each of which paired at most two operation types) might have missed.
