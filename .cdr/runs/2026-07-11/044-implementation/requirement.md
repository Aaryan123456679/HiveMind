# Requirement — Subtask 4.5.1.2

Issue #38 (Phase 4.5 engine/btree follow-ups), subtask 4.5.1.2:

**Add retry cap / livelock guard to crabbing TryLock restart-from-root loop**

- Acceptance criteria: `crabInsert`/`crabDeleteOnce`'s `errRestartFromRoot`
  retry loop has a bounded retry count (or exponential backoff cap) after
  which it surfaces an explicit error rather than retrying forever;
  documented as a theoretical (never observed) livelock mitigation, not a
  correctness fix.
- Test spec: `go test ./engine/btree/... -run TestCrabbingRetryCapSurfacesError`
  — inject a hook/mechanism forcing `TryLock` to always fail for a target
  node, assert the operation returns a bounded-retries error instead of
  hanging.
- Impacted modules: `engine/btree/insert.go`, `engine/btree/delete.go`,
  `engine/btree/insert_test.go`

## Scope boundary (explicit)

- Subtask 4.5.1.1 (dangling NextSibling splice fix) is already done/verified
  this session — do not touch.
- Do not touch `latch.go`'s eviction logic (4.5.1.3), `lookup.go`'s doc
  comment (4.5.1.4), or the test-only subtasks 4.5.1.5/4.5.1.6.
- `Tree.Lookup`'s analogous unbounded retry loop (in `lookup.go`, on
  `errOptimisticRetry`) is explicitly OUT of scope per the issue's own
  "Impacted modules" list for 4.5.1.2 — only `crabInsert` (insert.go) and
  `crabDelete`/`crabDeleteOnce` (delete.go) get the cap in this subtask.
- `latch.go`'s existing doc comment on `restartFromRootCount` asserts "none
  of these restart loops have, or should have, a maximum-attempt cap" —
  this is now stale for crabInsert/crabDelete after this change and needs a
  minimal, accuracy-only correction (not a behavioral change, not touching
  the eviction feature).

## Security note

`gh issue view 38`'s body was read fresh and found to contain no embedded
fake system-reminder/injection text — it is a clean, well-formed issue body.
No untrusted-instruction content encountered in the issue or git history
during this run.
