# Subtask 4.5.1.1 — Fix dangling cross-grandparent NextSibling after degenerate-ancestor splice

Source: GitHub issue #38 (Phase 4.5 follow-ups), regression id 2a.4.3, originally flagged
CHANGES_REQUESTED in run `2026-07-06-005-verification`.

Acceptance criteria (verbatim from issue #38):
- `spliceOutDegenerateAncestor` (engine/btree/delete.go) correctly patches the true left-neighbor
  `NextSibling` pointer even when the spliced ancestor is its grandparent's first child (`gj == 0`,
  cross-grandparent case), OR a descent-time safeguard is added so `crabDeleteOnce`/`crabInsertOnce`/
  `findParent`'s NextSibling move-right logic never trusts a dangling link into an abandoned,
  `Children`-unreachable node. No silent misrouting under concurrent descent.

Test spec: `go test ./engine/btree/... -race -run TestDeleteSpliceGj0CrossGrandparentNoDangling` —
deterministic hand-constructed tree forcing the `gj == 0` splice scenario, assert no dangling
`NextSibling` remains and concurrent descent never routes into the abandoned node.

Impacted modules: `engine/btree/delete.go`, `engine/btree/delete_test.go`.

Scope note: this run implements ONLY 4.5.1.1. It does not touch latch.go, insert.go's retry loop,
lookup.go's doc comment, or the other issue #38 subtasks (4.5.1.2-4.5.1.6).
