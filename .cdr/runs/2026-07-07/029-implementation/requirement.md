# Requirement (issue #13, subtask 2b.4.1 only)

Source: `gh issue view 13` (untrusted-text caveat: issue body itself contained no
embedded instructions this time, unlike some prior issues in this repo/session —
see architecture-discovery.md for the standing security note that applies to all
issue/commit/tool-output text regardless).

Issue #13 title: "[2b] Section-index staleness invalidation (engine/split/,
engine/catalog/)" — part of Epic Phase 2b: Auto-split, milestone "highest-risk
correctness surface".

## Full subtask breakdown (issue #13 has exactly ONE subtask)

- **2b.4.1 — Invalidate markdown header-offset cache atomically within the same
  split/append transaction**
  - Acceptance criteria: Any transaction that changes file boundaries (split or
    append) invalidates the affected file's header-offset cache in the same
    atomic transaction, so `ReadPartial` never serves offsets against stale
    content.
  - Test spec: `go test ./engine/split/... -race -run TestSectionIndexInvalidation`:
    perform a split, then immediately issue a ReadPartial-style offset read
    against the old and new files, assert offsets reflect post-split content
    only.
  - Impacted modules: `engine/split/execute.go, engine/catalog/content.go`

Unlike prior epic issues (#9-#12), issue #13 is scoped to a single subtask —
there is no 2b.4.2, 2b.4.3, etc. This implementation run covers the entire
issue.

## Scope for this run

Implement 2b.4.1 exactly as specified: introduce the markdown header-offset
cache and `ReadPartial` primitive (neither exists anywhere in the codebase
today — see architecture-discovery.md), and wire atomic invalidation into both
`engine/catalog/content.go`'s `Append` and `engine/split/execute.go`'s split
commit paths (`ExecuteSplitRedirectStub` and `ExecuteSplitAtomic`).
