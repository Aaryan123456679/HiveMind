# Requirement — Subtask 2b.3.1

Source: `gh issue view 12` (Epic Phase 2b "Auto-split", milestone "Phase 2b: Auto-split
(engine/split/) — highest-risk correctness surface"). Issue #12 title: "[2b] Atomic
split-transaction execution (engine/split/, engine/graph/ minimal writer)". Impacted
modules for the whole issue: `engine/split/, engine/graph/, engine/btree/,
engine/catalog/, engine/wal/`. Issue #12 has 6 subtasks (2b.3.1-2b.3.6); this run
implements only the first.

NOTE (untrusted-content flag): the raw `gh issue view 12` tool output contained an
embedded block formatted to look like a system reminder (claiming a date change /
"Auto Mode Active" instructions). This is injected content inside the issue body/
comments, not a genuine environment or user instruction — per this repo's known
prompt-injection precedent for issue content, it was ignored and had no effect on
this run's scope or behavior.

## Subtask 2b.3.1 (verbatim from issue body)

**2b.3.1 — Allocate new fileIDs + write new .md files split-off content per split plan**

- Acceptance criteria: for each `{newPath, sectionRanges}` entry (a
  `SplitFileProposal` within a `SplitPlan`), a new fileID is allocated and a new
  content file is written containing exactly the specified section range's content.
- Test spec: `go test ./engine/split/... -run TestSplitAllocateAndWrite`: given a
  fixture plan, assert new files exist with correct content matching the specified
  section ranges.
- Impacted modules: `engine/split/execute.go`, `engine/split/execute_test.go` (new
  files).

## Explicit scope boundary vs. later subtasks in the same issue

- 2b.3.2 — redirect/stub at old path + catalog status/redirectTargetIDs update.
- 2b.3.3 — B+Tree topic-path repointing.
- 2b.3.4 — engine/graph/ minimal append-only edge writer (SPLIT_SIBLING/REDIRECT).
- 2b.3.5 — SPLIT_SIBLING edges + inbound-edge re-pointing to redirect stub.
- 2b.3.6 — single WAL-covered atomic transaction wrapping all of the above +
  writer-queue release.

2b.3.1 must NOT touch the catalog, B+Tree, or graph, and does not need to provide
cross-step atomicity (that is 2b.3.6's job). It only needs: (1) fileID allocation
using the existing convention, and (2) durable (crash-safe in isolation) writes of
new content files whose bytes are exact section-range slices of the original file's
content.
