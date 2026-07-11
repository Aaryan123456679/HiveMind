# Subtask 4.5.3.4 (GitHub issue #40, milestone #10)

Pulled fresh via `gh issue view 40` on 2026-07-11.

## Title
4.5.3.4 — Add topic-path key normalization/namespace layer for B+Tree keys used
by split execution

## Acceptance criteria
`ExecuteSplitBtreeInsert` (engine/split/execute.go) inserts new/old topic paths
via a defined normalization/canonicalization layer instead of raw,
unnormalized path strings as direct B+Tree keys.

## Test spec
`go test ./engine/split/... -run TestSplitBtreeKeyNormalization`: insert paths
with equivalent-but-differently-formatted representations (e.g. trailing
separators), assert they normalize to the same canonical key.

## Impacted modules (per issue)
`engine/split/execute.go`, `engine/split/execute_test.go`

## Background (from .cdr/memory/pending.md, "Raw topic-path strings used
directly as B+Tree keys, no normalization/namespace layer" entry)
`ExecuteSplitBtreeInsert` (task-2b.3.3, issue #12) inserts new and old topic
paths into `*btree.Tree` using the raw path string as the key, with no
normalization, canonicalization, or namespace/prefix layer. Flagged as a
non-blocking finding during verification of task-2b.3.3 and forwarded to
Phase 4.5 for reconciliation once a canonical topic-path indexing convention
is designed. This subtask is that reconciliation.

## Note on scope within execute.go
The same raw-path-as-key pattern (`tree.Insert(path, fileID)`) also appears
inline in `ExecuteSplitAtomic`'s apply closure and in `RecoverSplitCommits`'s
replay loop, not only in `ExecuteSplitBtreeInsert` itself. All three sites are
within the single file named in the issue's "Impacted modules"
(`engine/split/execute.go`), and leaving the other two raw would create an
inconsistency where the same oldPath/newPath, run through
`ExecuteSplitBtreeInsert` vs. `ExecuteSplitAtomic`, could canonicalize
differently. All three call sites are therefore routed through one shared
normalization helper introduced in this subtask.
