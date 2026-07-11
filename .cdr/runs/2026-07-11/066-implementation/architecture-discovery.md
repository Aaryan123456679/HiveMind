# Architecture Discovery

Read the CURRENT `Tree.Lookup` doc comment and implementation in
`engine/btree/lookup.go` directly (not the issue text) to confirm exactly
what it claims and what it does, per this run's instructions.

## Current doc comment (lines 347-379, `func (t *Tree) Lookup` at line 380)

The doc comment states Tree.Lookup is:
- "lock-free, optimistic" and concurrency-safe against Tree.Insert/Delete
  and other Tree.Lookup calls "at the per-node latch level
  (NodeStore.Lock/TryLock in latch.go)" — explicitly scoped to per-node
  latches, not to every lock in the codebase.
- It then has an explicit, dedicated paragraph (lines 357-365):
  "One narrow, intentional exception: this function's retry loop calls
  t.Root() on every attempt (see below), which briefly acquires rootMu --
  the same tree-level mutex Tree.Insert/Tree.Delete use for root-bootstrap
  and root-split. This is a tree-level, not a per-node, lock, is held only
  for the duration of a single map-free field read, and is contended only
  at the rare instant a root change is actually happening; it does not
  affect per-node lock-freedom or produce incorrect results, but it does
  mean this function is not literally "never calls Lock/TryLock anywhere" --
  only "never takes a per-node latch"."

This is exactly the acceptance criterion asked for: it "accurately describes
its actual locking behavior (a single brief rootMu acquisition), not
'never locks.'"

## Implementation check (confirms the rootMu-acquisition claim directly)
- `func (t *Tree) Lookup(path string)` (line 380) calls `t.Root()` on every
  retry-loop iteration.
- `t.Root()` (engine/btree, Tree type in btree.go/latch.go — the tree-level
  root accessor) acquires `t.rootMu` (RLock) to read the current root node
  ID, exactly matching the doc comment's description: a single, brief,
  tree-level `rootMu` acquisition per attempt, not a per-node latch, and not
  "no locking at all."
- `readNodeOptimistic` (line 227) and `lookupOnce` (line 251), which do the
  actual per-node descent work, never call `NodeStore.Lock`/`TryLock`
  (confirmed by reading their bodies and the explicit doc-comment statement
  at lines 222-226).

## History: was this already fixed by commit 3ef7cde?
`git log -S "never calls Lock or TryLock" -- engine/btree/lookup.go` and a
diff of `3ef7cde` ("docs(btree): fix stale version-field/lock-free doc
comments + add restart-attempt observability counter") show that commit
replaced an OLDER doc comment that read:

  "... concurrent Tree.Lookup calls: it never calls NodeStore.Lock or
  TryLock, so it can never block a writer and can never be blocked by one
  (this subtask's, 2a.4.4's, acceptance criterion)."

with the current, scoped wording quoted above, explicitly adding the
rootMu/t.Root() exception paragraph. That older wording is the "never
locks"-style overclaim issue #38's subtask 4.5.1.4 refers to (it claimed
Tree.Lookup, as a whole, never calls Lock/TryLock anywhere, when in fact
t.Root() briefly takes rootMu).

`grep -n "never lock\|never call.*Lock\|rootMu" engine/btree/lookup.go`
against the CURRENT file shows no remaining "never locks"-style overclaim:
only the accurate, scoped "never calls Lock or TryLock" statement about
readNodeOptimistic specifically (line 222, still true and not what the
subtask flags), plus the rootMu exception paragraph (lines 358-365)
that this subtask asks for.

## Conclusion
Subtask 4.5.1.4 is already fully resolved by commit 3ef7cde
(2026-07-07, "docs(btree): fix stale version-field/lock-free doc comments +
add restart-attempt observability counter"). No further source edit to
`engine/btree/lookup.go` is needed or warranted; making a cosmetic edit to
already-accurate text would risk introducing drift. This follows the same
"already fixed, re-confirm" pattern used elsewhere in this milestone (e.g.
run `035-implementation`/commit `ed57468` for subtask 4.5.11.1, and run
`056-implementation`/commit `0b64783` for subtask 4.5.2.2).
