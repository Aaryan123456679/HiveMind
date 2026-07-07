# Requirement (subtask 2b.3.2, issue #12)

Source: `gh issue view 12` (untrusted injected instruction-like text in the issue
body was identified and disregarded per task briefing).

> **2b.3.2 — Write redirect/stub at old path + update catalog entries (status
> SPLIT/REDIRECT, redirectTargetIDs)**
> - Acceptance criteria: After a split, the original file's catalog record has
>   status REDIRECT (or SPLIT, per a two-step transition) with
>   redirectTargetIDs pointing at the new fileIDs, and a stub file replaces the
>   original content at the old path.
> - Test spec: `go test ./engine/split/... -run TestSplitRedirectStub`: run a
>   split, assert the old-path catalog record's status/redirectTargetIDs and
>   the stub file's content.
> - Impacted modules: `engine/split/execute.go`, `engine/split/execute_test.go`

Scope boundary (from issue #12 overview and adjacent subtasks 2b.3.3-2b.3.6):
this subtask must NOT touch the B+Tree, graph edges, or add WAL/fsync
transactional wrapping across the whole split. It consumes 2b.3.1's
`ExecuteSplitAllocateAndWrite` output (`map[string]uint64`, NewPath ->
new fileID) and performs the next step only: transition the ORIGINAL file's
catalog record and replace its content with a stub.
