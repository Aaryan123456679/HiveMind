# Subtask 4.5.18.4 — Implementation Plan

## 1. Requirement
Issue #57, subtask 4.5.18.4 (LOW): correct the commit message/diff attribution
mismatch on commit `1aaf2f7` (falsely claims `engine/rpc/server.go` /
`lookupPathForFileID` changes that actually landed in `14902e8`). Doc-only
correction in `.cdr/memory/pending.md`; no history rewrite, no production
code changes.

## 2. Architecture discovery
`.cdr/memory/pending.md` already has a full section titled
"## Commit-hygiene finding (2026-07-11, for milestone #10 .cdr/ audit pass,
task #21)" (lines 91-92) documenting this exact finding. No LLD/index touch
needed — this is a `.cdr/memory` note, not a code change.

## 3. Impact analysis
Only `.cdr/memory/pending.md` is touched (append-only, surgical). No source,
no tests, no other docs. File is shared/concurrently edited per orchestrator
note — re-read latest content immediately before editing.

## 4. Independent re-verification of the underlying claim (before touching anything)
Ran the following directly against the repo (not trusting the existing note):

- `git show --stat 1aaf2f7 -- engine/rpc/server.go` → **no output** (file
  untouched in that commit). Confirmed.
- `git show --stat 14902e8 -- engine/rpc/server.go` → real changes
  (`engine/rpc/server.go | 168 +++...`, part of "fix(rpc): index new files'
  paths into pathIndex on PutSegment create (issue #43, 2/3)"). Confirmed.
- `git log --oneline --all -S"lookupPathForFileID" -- engine/rpc/server.go`
  → only `14902e8`. Confirms `lookupPathForFileID` was introduced/touched in
  `server.go` at `14902e8`, not `1aaf2f7`.
- `git log --oneline --all -S"GetFile_PathIndexMiss"` and
  `-S"GetFile_NilPathIndex"` → both only in `1aaf2f7` (in
  `engine/rpc/server_test.go`, a different file from `server.go`). So the two
  named tests genuinely are part of `1aaf2f7`'s diff; only the `server.go` /
  `lookupPathForFileID` claim is the misattribution.
- `git show -s --format='%B' 1aaf2f7` → confirms the message literally says
  "...populated server-side via a best-effort reverse pathIndex scan
  (engine/rpc/server.go's lookupPathForFileID)..." — the false claim.
- Commit-distance check: `git log --oneline 1aaf2f7..14902e8` →
  `14902e8`, `9774bee`, `5be2c91` (3 commits, linear history, confirmed via
  `git log --graph --oneline 1aaf2f7~1..14902e8`). **This is 3 commits after
  `1aaf2f7`, not 6** as stated in both the GitHub issue body and the existing
  pending.md note. This is a minor factual discrepancy to correct while
  tightening the note.

Verdict: the existing pending.md note's core claim (server.go misattributed,
real change landed in 14902e8, functionally harmless, no history rewrite) is
**accurate**. One minor factual detail ("6 commits later") is **incorrect**
and is corrected to "3 commits later" in the resolution note. The note also
lacked explicit reproduction commands and a closing "no action needed beyond
this note" statement — both added.

## 5. Validation matrix
| Check | Method | Result |
|---|---|---|
| `1aaf2f7` does not touch `server.go` | `git show --stat 1aaf2f7 -- engine/rpc/server.go` | confirmed empty |
| `14902e8` does touch `server.go` | `git show --stat 14902e8 -- engine/rpc/server.go` | confirmed real diff |
| `lookupPathForFileID` origin in `server.go` | `git log -S"lookupPathForFileID" -- engine/rpc/server.go` | only `14902e8` |
| Named tests are genuinely part of `1aaf2f7` | `git log -S"GetFile_PathIndexMiss"` / `-S"GetFile_NilPathIndex"` | only `1aaf2f7`, in `server_test.go` |
| Commit distance `1aaf2f7` → `14902e8` | `git log --oneline 1aaf2f7..14902e8` | 3 commits, not 6 |
| Both commits already pushed (no rewrite needed) | `git log --oneline origin/main` contains both | yes |
| pending.md edit is additive/surgical | manual diff review before commit | single appended line, no reflow of existing section |

## 6. Implement
Append a short "Resolution note" line to the existing section in
`.cdr/memory/pending.md`, correcting "6 commits" -> "3 commits", adding the
exact `git show --stat` reproduction commands, and a closing "no action
needed beyond this note" statement.

## 7. Self-consistency (internal only, not verification)
- Confirmed edit is additive (git diff shows only insertion, no deletions to
  pre-existing lines).
- Re-read the file immediately before editing to avoid clobbering concurrent
  edits (orchestrator note).
- No code/build to check (doc-only change) — N/A for build-green check.

## 8. Commit
One local commit, `.cdr/memory/pending.md` + this run's artifacts, no push.

## 9. Handoff
`handoff.json` with pointers only (file path + line range of the new note),
recommending `/cdr:verify --subtask 4.5.18.4` next.
