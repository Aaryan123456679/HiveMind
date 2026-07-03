# Requirement — Subtask 1.2.6

Source: `gh api repos/Aaryan123456679/HiveMind/issues/2 -q .body`, checklist item verbatim:

> - [ ] **1.2.6 — Persisted reload correctness test (disk round-trip)**
>   - Acceptance criteria: A tree built, written to index/name.idx, closed, and reopened yields
>     identical lookup/prefix-scan results as before closing.
>   - Test spec: go test ./engine/btree/... -run TestPersistReload: build tree, close, reopen file,
>     assert lookup/prefix-scan parity.
>   - Impacted modules: `engine/btree/btree_test.go`

Confirmed: test file is `engine/btree/btree_test.go`, test name is `TestPersistReload` — matches
1.2.5's implementer's secondhand note, verified directly from the issue body (not from
`gh issue view`, which a prior agent found truncates/garbles this checklist item).

This is the LAST subtask of task 1.2 (B+Tree). 1.2.1-1.2.5 are all `verified` per
`.cdr/index/task.jsonl`.

## Known gap this subtask must close

Per `engine/btree/insert.go`'s `NodeAllocator` doc comment (lines 32-37) and regression notes
`024-verification` (1.2.3) / `030-verification` (1.2.4) / implicitly assumed by `032-verification`
(1.2.5): the root node ID is never persisted across process restarts. `NodeAllocator` already
persists its own high-water-mark via a `.nodealloc` sidecar file (WriteAt + Sync idiom), but
nothing persists "the current root node ID" — callers currently must track `newRootNodeID`
themselves in memory only. A literal "close the file, reopen it, and still find the tree" test is
impossible to write today without this, since there's no way to recover the root ID after
reopening. This subtask adds that persistence (`<index-path>.root` sidecar, mirroring
`.nodealloc`'s exact durability idiom) and the round-trip test.
