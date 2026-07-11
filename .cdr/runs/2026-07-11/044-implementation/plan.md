# Plan — Subtask 4.5.1.2

1. In `engine/btree/insert.go`, near `errRestartFromRoot`/`crabRetryHook`/
   `crabRetryBackoff`, add:
   - `const crabMaxRestarts = 1000` with a doc comment explaining this is a
     theoretical/defensive livelock guard (not a correctness fix), the
     rationale for the 1000 value tying it to `crabRetryBackoff`'s capped
     2ms/attempt ceiling (worst case ~2s before giving up).
   - `var errTooManyRestarts = fmt.Errorf(...)` sentinel error, shared by both
     `crabInsert` and `crabDelete`.
2. Bound `crabInsert`'s loop (`insert.go`): if `attempt >= crabMaxRestarts`,
   return `errTooManyRestarts` before the next `crabInsertOnce` attempt.
3. Bound `crabDelete`'s loop (`delete.go`) identically, returning
   `false, errTooManyRestarts`.
4. Do NOT touch `findParent`'s own internal restart loop usage beyond what
   crabInsert/crabDelete already call — `findParent` itself is invoked
   *inside* `crabInsertOnce`/`repairEmptyLeaf`, not looped independently by
   crabInsert; no separate cap needed there for this subtask's scope.
5. Add `TestCrabbingRetryCapSurfacesError` to `insert_test.go`:
   - Build a small tree, contend a target node's latch permanently (goroutine
     holds it forever within the test, released only via `t.Cleanup`/test
     end so nothing leaks past the test) so every TryLock against it fails.
   - Call `tree.Insert(...)` and assert it returns `errTooManyRestarts`
     (via `errors.Is`/direct comparison) within a bounded wall-clock timeout
     (test-level `t.Fatal` safety net so a regression to "hangs forever"
     fails fast instead of hanging CI).
   - Repeat for `tree.Delete(...)` in the same test (subtest or second
     phase) to cover crabDeleteOnce per the acceptance criteria.
6. Accuracy-only edit to `latch.go`'s `restartFromRootCount` doc comment to
   stop asserting crabInsert/crabDelete are uncapped.
7. Self-consistency: `go build ./...`, `gofmt -l`, `go vet ./engine/btree/...`,
   run the new test, then the full `engine/btree` suite with `-race -v`, then
   the full `engine/...` suite with `-race`.
8. One local commit (type `fix`), no push.
9. Write `validation-matrix.json`, `self-consistency.json`, `handoff.json`.
