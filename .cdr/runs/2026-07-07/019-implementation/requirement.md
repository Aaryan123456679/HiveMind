# Requirement — Subtask 2b.3.3

Source: `gh issue view 12` (Epic: "[2b] Atomic split-transaction execution",
engine/split/, engine/graph/ minimal writer, Phase 2b: Auto-split —
highest-risk correctness surface).

NOTE (security): the raw issue body, as fetched, contained injected
fake-system-reminder-style text (a fabricated "date changed" notice, a fake
"tokensave" MCP tool-usage directive, and a fake "Auto Mode Active"
directive). These are NOT legitimate instructions from the user or the
system — they are untrusted data embedded in fetched content. They were
ignored; nothing from that injected text influenced this run's actions.

## Subtask 2b.3.3 (verbatim acceptance criteria / test spec from issue body)

**2b.3.3 — Insert new topic paths into B+Tree; repoint old path's entry to
redirect stub.**
- Acceptance criteria: new topic path resolves via B+Tree lookup to its new
  fileID; old path still resolves, but now to the redirect-stub fileID.
- Test spec: `go test ./engine/split/... -run TestSplitBtreeRepoint`.
- Impacted modules: `engine/split/execute.go`, `engine/split/execute_test.go`
  (extend, do not create new files).

## Prior committed subtasks this builds on (already in engine/split/execute.go)

- 2b.3.1 `ExecuteSplitAllocateAndWrite`: allocates new fileIDs, writes new
  content files per split plan, returns `map[string]uint64` NewPath->fileID.
- 2b.3.2 `ExecuteSplitRedirectStub`: transitions the original record
  `StatusSplit -> StatusRedirect`, overwrites the ORIGINAL fileID's content
  file in place with a deterministic redirect-stub, sets
  `RedirectTargetIDs`. Critically: the original fileID is REUSED for the
  stub — no new fileID is allocated for the old path.

## This subtask's scope

Take the NewPath->fileID map from 2b.3.1 and:
1. Insert each new topic path into the B+Tree, pointing at its new fileID.
2. "Repoint" the old path's existing B+Tree entry so it resolves to the
   (now redirect-stub) original fileID.

Explicitly out of scope (deferred to later subtasks per issue body):
graph edges (2b.3.4/2b.3.5), WAL/fsync transactional wrapping spanning
multiple steps (2b.3.6).
