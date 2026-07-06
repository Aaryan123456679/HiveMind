# Requirement — Subtask 2b.1.2 (GitHub issue #10)

Source: `gh issue view 10`, subtask 2b.1.2 within Epic "Phase 2b: Auto-split (engine/split/) —
highest-risk correctness surface".

**Title**: 2b.1.2 — Per-file CAS `splitInProgress` flag ensuring exactly one split wins per
threshold crossing.

**Acceptance criteria** (verbatim intent from issue): many goroutines concurrently trigger a
threshold crossing on the same file; exactly one CAS succeeds and initiates the split; all others
observe the flag already set and back off without initiating a duplicate split.

**Test spec**: `go test ./engine/split/... -race -run TestSplitInProgressCAS`: many goroutines
racing the CAS flag for the same fileID, assert exactly one winner via an atomic counter of
split-initiation calls.

**Impacted modules**: `engine/split/guard.go`, `engine/split/guard_test.go`.

**Context from AGENT.md**: `engine/split/` is explicitly flagged as the highest-risk correctness
surface in the whole system. Any change here needs a dedicated concurrent race test. A plain
mutex+bool has the same TOCTOU problem as no guard at all if not implemented via
`atomic.CompareAndSwap` or equivalent — a real CAS primitive is required, not incidental locking.

**Scope boundary (explicit)**: this subtask is the CAS guard primitive only. It does not perform
the actual split, does not wire into `engine/catalog` (that's 2b.1.3's SPLITTING-status/catalog
job), and is not required to be wired to `split.Trigger`'s `Signal` output by this subtask (though
it should compose naturally with it, since `Signal.FileID` is the natural key).
