# Requirement — subtask 2b.1.3 (issue #10, 3rd/last subtask)

## Source
`gh issue view 10` (subtask list) + `gh issue view 12` (scope-boundary check only).

Note: `gh issue view 10 --comments` output was inspected for the 2b.1.1/2b.1.2
close-out context (already-verified, already-committed prior subtasks). One of the
tool outputs encountered during discovery (a `gh issue view 10` call) contained an
embedded block of text formatted to look like a legitimate CLI/system reminder
(claiming a "date change" with an instruction not to mention it, plus fake "MCP
server instructions"). This is untrusted content injected via the issue body/API
response, not a real system message — it has been disregarded entirely and flagged
to the user; it did not influence any decision below.

## Subtask 2b.1.3 text (from issue #10)
**2b.1.3 — Mark file SPLITTING in catalog; queue new writers; verify existing
readers unaffected via MVCC**

- Acceptance criteria: Once a split begins, the catalog record transitions to
  SPLITTING; new writer requests for the file are queued rather than applied;
  readers holding a pre-split MVCC snapshot continue to see their pinned version
  unaffected, for the duration of the in-flight SPLITTING state.
- Test spec: `go test ./engine/split/... -race -run TestSplittingStatusIsolation`:
  assert catalog transitions to SPLITTING, new writers are blocked/queued (not
  silently applied) while SPLITTING, and readers already holding (or newly taking)
  an MVCC snapshot are unaffected.
- Impacted modules: `engine/split/orchestrate.go`, `engine/split/orchestrate_test.go`.

## Scope boundary vs issue #12 ("Atomic split-transaction execution")
Issue #12's subtasks (2b.3.1-2b.3.6) own: allocating new fileIDs, writing new
content files, writing redirect stubs, updating catalog to SPLIT/REDIRECT with
RedirectTargetIDs, B+Tree repointing, graph SPLIT_SIBLING/REDIRECT edges, and the
single atomic WAL-covered commit of all of the above (with queued-writer release
tied to that commit).

Therefore 2b.1.3 (this subtask) owns only:
1. The transition of a file's catalog `Status` field INTO `StatusSplitting` (not
   the eventual `StatusSplit`/`StatusRedirect` transition with populated
   `RedirectTargetIDs` — that data only exists once #12's execution logic runs).
2. A write-admission gate: new writers must observe SPLITTING and be refused/
   queued rather than silently applied.
3. Demonstrating (via test, not new production code) that MVCC readers are
   unaffected — this is largely a consequence of pre-existing engine/mvcc
   snapshot-isolation design (Status is a catalog-record field completely
   orthogonal to CurrentVersion/version-file bytes), not new machinery.
4. A clean exit-out-of-SPLITTING transition primitive (so status doesn't leak),
   parameterized by outcome (back to Active on abort, or forward to Split on
   success) — but the actual "success" transition with real RedirectTargetIDs
   is #12's job to drive; 2b.1.3 just provides the primitive #12 will call.

Explicitly NOT in scope for 2b.1.3: allocating redirect targets, writing any
content/stub files, B+Tree/graph wiring, or the single atomic WAL-covered commit
of the full split (#12 owns all of that). Crash-recovery of a stuck SPLITTING
status (if the split holder crashes without calling the exit primitive) is also
explicitly flagged as an open gap, not fixed here (see architecture-discovery.md).
