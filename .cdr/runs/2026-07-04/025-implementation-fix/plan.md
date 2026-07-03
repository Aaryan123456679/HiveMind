# Plan: 1.2.3 follow-up — add TestInsertSplit acceptance-test entry point

## Requirement
`.cdr/runs/2026-07-04/024-verification/verification.json` returned
CHANGES_REQUESTED for exactly one blocking finding: GitHub issue #2's literal
acceptance test spec for subtask 1.2.3 is
`go test ./engine/btree/... -run TestInsertSplit`, but no test function
named or substring-matching `TestInsertSplit` exists in
`engine/btree/insert_test.go`. Running the literal spec command yields
`testing: warning: no tests to run` with exit 0 -- a silent false-pass.

## Architecture discovery
`engine/btree/insert_test.go` already has `TestInsertLeafSplit` and
`TestInsertInternalSplit`, each self-contained (own store/allocator setup,
own assertions via `assertAllLookupable`/`assertStructuralInvariants`). No
other file references these two test names by string (checked via grep), so
renaming/wrapping is safe.

## Impact analysis
- Touches only `engine/btree/insert_test.go`.
- No production code, no other test file, no other subtask's coverage
  affected.
- All existing test names (`TestInsertLeafSplit`, `TestInsertInternalSplit`)
  are preserved as top-level runnable tests (thin wrappers), so no existing
  `-run` invocations elsewhere break.

## Plan
1. Extract the body of `TestInsertLeafSplit` into an unexported helper
   `testInsertLeafSplit(t *testing.T)`; `TestInsertLeafSplit` becomes a thin
   call to it.
2. Extract the body of `TestInsertInternalSplit` into an unexported helper
   `testInsertInternalSplit(t *testing.T)`; `TestInsertInternalSplit` becomes
   a thin call to it.
3. Add a new top-level `TestInsertSplit(t *testing.T)` that runs both via
   `t.Run("LeafSplit", testInsertLeafSplit)` and
   `t.Run("InternalSplit", testInsertInternalSplit)`.
4. No other file changed. No production code changed.

## Validation matrix
| Requirement | Test |
|---|---|
| `go test ./engine/btree/... -run TestInsertSplit` matches >=1 test and runs real split assertions | `TestInsertSplit` (new), subtests `LeafSplit`/`InternalSplit` |
| No regression to existing named tests | `TestInsertLeafSplit`, `TestInsertInternalSplit` still present and pass |
| No regression to rest of package | Full `go test ./engine/btree/... -race -v` |

## Self-consistency (internal only -- not verification)
- `go build ./engine/...` -- pass
- `go vet ./engine/...` -- pass
- `go test ./engine/btree/... -run TestInsertSplit -race -v` -- pass (2/2 subtests: LeafSplit, InternalSplit)
- `go test ./engine/btree/... -race -v` -- pass (full package, no regressions)
