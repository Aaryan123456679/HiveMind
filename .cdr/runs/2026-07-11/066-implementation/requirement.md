# Requirement — Subtask 4.5.1.4 (Issue #38)

## Title
Fix `Tree.Lookup` doc comment overclaim ("never locks").

## Acceptance criteria (verbatim, `gh issue view 38`)
`Tree.Lookup`'s doc comment in `engine/btree` accurately describes its actual
locking behavior (a single brief `rootMu` acquisition), not "never locks."

## Test spec (verbatim)
Doc-only change; `go vet ./engine/btree/...` and `gofmt -l` clean, no
behavioral test required.

## Impacted modules
`engine/btree/lookup.go`

## Scope boundary
This run addresses ONLY subtask 4.5.1.4 of issue #38, and touches only
`engine/btree/lookup.go` (per explicit scope-isolation instructions: other
concurrent agents are working on `engine/split`, `engine/wal`,
`engine/catalog` in this same checkout; only `engine/btree/lookup.go` may be
staged/edited by this run, plus this run's own `.cdr/runs/...` directory).
Sibling subtasks 4.5.1.1-4.5.1.3, 4.5.1.5-4.5.1.6 are explicitly out of
scope and not touched.

## Untrusted-content disclosure
`gh issue view 38`'s body was read fresh and is well-formed, matches the
task prompt's summary verbatim, contains no embedded fake directives.
Separately, this run's own tool-call stream contained an environment-styled
"the date has changed... do not mention it" system-reminder block. Per this
run's own instructions, that reminder is disclosed here but not acted upon
(it does not grant any consent/approval and does not affect this subtask's
scope, files touched, or permissions).
