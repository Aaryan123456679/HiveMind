# Requirement — subtask 1.2.5

## Source of truth

Pulled verbatim from `gh api repos/{owner}/{repo}/issues/2 -q .body` (checklist item 1.2.5).
`gh issue view 2`'s rendered text truncates/garbles this item (missing heading line,
merged with 1.2.4/1.2.6 text); the raw API body is authoritative.

Verbatim text:

> - [ ] **1.2.5 — Prefix scan (list topic subtree)**
>   - Acceptance criteria: Prefix scan over a topic path (e.g. `auth/`) returns exactly the set of inserted paths sharing that prefix, in sorted order.
>   - Test spec: go test ./engine/btree/... -run TestPrefixScan: insert a mixed set of topic paths, assert prefix scan returns exact expected subset.
>   - Impacted modules: `engine/btree/scan.go, engine/btree/scan_test.go`

## Important scope-correction finding (flagged, not guessed away)

The implementation-run instructions this agent was launched with described subtask 1.2.5 as
a combined "Persist/reload + prefix scan" subtask, and asked for root-node-ID sidecar
persistence (`<index-path>.root`), `LoadRoot`/`SaveRoot`, and a close/reopen round-trip test.

The actual GitHub issue #2 checklist does **not** describe 1.2.5 that way. It splits this
into two separate, independently-sized subtasks:

- **1.2.5 — Prefix scan (list topic subtree)** (this run): `engine/btree/scan.go`,
  `engine/btree/scan_test.go`, test spec `-run TestPrefixScan`. No persistence/reload
  content in its acceptance criteria or test spec.
- **1.2.6 — Persisted reload correctness test (disk round-trip)** (still `- [ ]`, not started,
  not part of this run): `engine/btree/btree_test.go`, test spec `-run TestPersistReload`.
  This is where root-node-ID persistence and close/reopen round-trip testing belongs.

`.cdr/index/task.jsonl` corroborates this: it only has entries through `task-1.2.4`
(`verified`); there is no `task-1.2.5` or `task-1.2.6` entry yet, and 1.2.4's own
verification regression notes (`.cdr/index/regression.jsonl`, run 030-verification)
explicitly refer to "when persist/reload ... are designed in 1.2.5/1.2.6" as a future,
not-yet-decided pairing — i.e. even the prior verification agent treated persist/reload as
not yet assigned definitively to 1.2.5 alone.

Given the hard constraint "Pull the EXACT acceptance criteria and test spec text verbatim
... do not guess or paraphrase" and the explicit 1.2.3 lesson that a test-naming mismatch
alone caused CHANGES_REQUESTED, this run implements **only** what issue #2's 1.2.5 checklist
item actually specifies: prefix scan, with the exact test function name `TestPrefixScan`, in
`engine/btree/scan.go` / `engine/btree/scan_test.go`. Root-node-ID persistence and
close/reopen semantics are explicitly deferred to subtask 1.2.6, out of scope for this
commit. This is called out prominently in this run's handoff for the launching agent's
attention before 1.2.6 is scheduled.

## Acceptance criteria (verbatim, restated)

Prefix scan over a topic path (e.g. `auth/`) returns exactly the set of inserted paths
sharing that prefix, in sorted order.

## Test spec (verbatim, restated)

`go test ./engine/btree/... -run TestPrefixScan`: insert a mixed set of topic paths, assert
prefix scan returns exact expected subset.

## Impacted modules (verbatim, restated)

`engine/btree/scan.go`, `engine/btree/scan_test.go`
