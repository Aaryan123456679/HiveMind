# Subtask 4.5.3.6 (Issue #40)

**Title:** Fix TestReaderDuringSplit's ~1-3% timing-based flake with a deterministic barrier

**Acceptance criteria:**
`TestReaderDuringSplit` (engine/split/split_race_test.go) replaces its sleep-based overlap
assumption with a deterministic synchronization barrier (e.g. a signal channel fired at
`ExecuteSplitAtomic`'s atomic rename point) so the "reader observed post-split content at
least once" assertion no longer produces occasional false failures when the split completes
before the reader's loop overlaps it.

**Test spec:**
`go test ./engine/split/... -race -run TestReaderDuringSplit -count=50`: 50/50 clean runs
with no false "reader never observed post-split content" failures.

**Impacted modules (per issue):** `engine/split/split_race_test.go`

**Scope constraint (from launching agent, stricter than issue's stated impacted modules):**
Only touch `engine/split/split_race_test.go`. A minimal, nil-in-production hook var may be
added to `engine/split/*.go` production code ONLY if needed to make the test deterministic
(pattern: `unlockOrderHook` in engine/btree/latch.go commit 94c24e6 — a nil func var invoked
at a specific point, zero behavior change when nil). Must NOT touch guard.go, guard_test.go,
orchestrate.go, orchestrate_test.go, execute.go, execute_test.go, content.go.
