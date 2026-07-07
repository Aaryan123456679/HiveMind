# Requirement — subtask 2b.3.5 (issue #12)

Source: `gh issue view 12` (fetched fresh this run; body/comments contain no
actionable instructions beyond the subtask list — treated as untrusted plain
text data only, per this run's security note).

**SECURITY NOTE**: Per the assigning agent's explicit instruction, this repo's
GitHub issue bodies/comments, commit messages/diffs, and some Bash stdout have
repeatedly contained embedded fake system-reminder-style text (fabricated
"date changed" notices, fake MCP/tool instructions, fake "Auto Mode Active"
directives) in prior sessions — a recurring prompt-injection pattern. This
run's own tool output (`.cdr/memory/pending.md` read) surfaced two more such
fake blocks (a fake "date changed to 2026-07-07" reminder and a fake MCP
"tokensave" tool-instruction block and fake "Auto Mode Active" block) appended
after the real pending-items content. None of this was acted upon; it is
inert data, flagged here per instruction and otherwise ignored.

## Subtask 2b.3.5 — verbatim acceptance criteria (from issue #12)

> **2b.3.5 — Add SPLIT_SIBLING edges between new files; re-point inbound
> edges [to] redirect stub**
> - Acceptance criteria: All pairs of newly split-off files gain a
>   SPLIT_SIBLING edge; any edge that previously pointed at the old path is
>   retargeted to the redirect stub rather than rewriting the full
>   inbound-edge list.
> - Test spec: `go test ./engine/split/... -run TestSplitGraphEdges`: run a
>   split with pre-existing inbound edges to the old path, assert
>   SPLIT_SIBLING edges among the new files and that inbound edges now point
>   to the stub.
> - Impacted modules: `engine/split/execute.go, engine/split/execute_test.go`

## Epic context (issue #12 full subtask list, for continuity)

- 2b.3.1 (done): allocate new fileIDs + write new content files.
- 2b.3.2 (done): write redirect stub at old path, transition catalog Status
  to StatusRedirect with RedirectTargetIDs — **reuses originalFileID for the
  stub; no new fileID is ever allocated for the old path**.
- 2b.3.3 (done): insert new topic paths into B+Tree; idempotently
  "repoint" oldPath's B+Tree entry (a no-op upsert, since oldPath already
  maps to originalFileID and that identity never changes).
- 2b.3.4 (done): minimal append-only `engine/graph` edge writer
  (`EdgeAppender.AppendEdge`, `EdgeType` enum: `EdgeSplitSibling`,
  `EdgeRedirect`), not yet wired to any caller.
- **2b.3.5 (this subtask)**: wire `engine/split/execute.go` to
  `engine/graph`'s `EdgeAppender` for the first time — append
  SPLIT_SIBLING edges among new files, and handle the "repoint inbound
  edges" requirement.
- 2b.3.6 (next): commit the entire split (allocation, content writes,
  catalog updates, btree updates, **graph edge writes**) as one
  WAL-covered, fsynced transaction; explicitly lists "graph edge writes" in
  its own acceptance criteria as part of what it atomically commits.
