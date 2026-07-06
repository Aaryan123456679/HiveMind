# Plan — 2a.4.3 Latch-crabbing delete

## Descent
Reuse `errRestartFromRoot`, `crabRetryBackoff`, `crabRetryHook`, `t.findParent`
directly from insert.go (already generic). Duplicate (not refactor)
`crabInsertOnce`'s window-of-2 TryLock descent into a new `crabDeleteOnce` in
delete.go, since `crabInsertOnce` is not generic (insert-specific leaf
mutation) and insert.go must not be touched. Descent ends holding the target
leaf's latch alone (parent already released, matching insert's discipline).

## Leaf-level repair (3-latch window)
On leaf key removal, if the leaf becomes empty (tombstone-trigger, matching
Phase-1 `Delete`'s policy) and is not the tree's root, `repairEmptyLeaf`:
1. `t.findParent` (fresh, generic, reused) to locate the CURRENT parent.
2. Blocking `Lock(parentID)` — isolated acquisition, nothing else held,
   mirrors `propagate`'s `Lock(parentID)`. Safe by the same proof: only ever
   blocks while holding zero other latches.
3. `TryLock(leafID)` (parent+leaf window, same as insert's window-of-2). Miss
   -> release parent, `errRestartFromRoot`.
4. Re-read leaf under its own latch (matches insert's "always read a node
   while holding its latch" discipline); if refilled concurrently, abort
   (no-op, safe).
5. Decide borrow-left / borrow-right / merge-left / merge-right, exactly
   mirroring Phase-1 `repairEmptyLeaf`'s policy (not redesigned). Borrow needs
   no 3rd latch beyond parent+leaf+sibling-being-borrowed-from (same 3-node
   budget). Merge needs the SAME 3-latch window (parent, empty leaf, chosen
   sibling) — this is delete's genuinely wider window vs insert's 2, because
   merging two leaves plus fixing the parent's separator/child entry touches
   three distinct nodes at once. The 3rd latch (sibling) is acquired via
   `TryLock` only, with full release of all 3 and restart-from-root on miss —
   never a blocking `Lock` on a 2nd/3rd node. This preserves the same
   deadlock-free-by-construction property insert.go established.

## Ancestor cascade (parent degenerates to 0 keys / 1 child)
Structurally identical in shape to `propagate`'s climbing loop (1 latch at a
time: `rootMu` check, `findParent`, blocking `Lock` on a freshly-identified,
otherwise-unheld ancestor). No 3rd latch needed here — this is the mirror of
insert's overflow-propagation, just removing a child pointer instead of
inserting one, and stopping at "not degenerate" instead of "not overflowing".
Root collapse uses `rootMu`, exactly mirroring insert's root-splitting.

Fixes a latent bug carried in the single-threaded Phase-1 delete.go: its
`InternalNode{Keys:..., Children:...}` reconstructions on shrink/splice drop
`NextSibling`/`LowKey`. Not fixed in the single-threaded path (out of scope,
untouched) but must not be reproduced in the new concurrent path, since
concurrent test scenarios readily produce multi-node internal levels where
this would break the NextSibling chain invariant.

## Documented gap (deferred, same spirit as 2a.4.2's "no retry cap")
When a degenerate ancestor is spliced out of its grandparent, this subtask
also patches the immediate left same-grandparent sibling's `NextSibling` to
skip over it. If the spliced node's true left neighbor in the level's
NextSibling chain instead belongs to a *different* grandparent's subtree,
that cross-grandparent link is left dangling (references an abandoned node
ID). Fixing this fully needs either backward pointers or a per-level scan,
neither of which exists yet. Low risk: never affects top-down
lookup/insert/delete correctness (nothing ever follows a stale NextSibling
into an already-unreachable node); only a structural-invariant chain-walk
could observe it. Flagged for 2a.4.4/2a.4.5.
