# Architecture discovery

## Original regression (2a.4.3 / 2026-07-06-005-verification)
`.cdr/index/regression.jsonl` line for subtask 2a.4.3: `spliceOutDegenerateAncestor` only patched a
same-grandparent left neighbor (`gj > 0`); when the spliced ancestor was its grandparent's FIRST
child (`gj == 0`), the true left neighbor lived under an adjacent grandparent and was never located
or patched, leaving a permanently dangling `NextSibling` — load-bearing because `crabDeleteOnce` /
`crabInsertOnce` / `findParent` all dereference `NextSibling` for move-right routing during
concurrent descent.

## CRITICAL FINDING: the code fix already shipped and was already independently verified PASS
`git log --oneline -- engine/btree/delete.go` shows:
- `b46d466` — original 2a.4.3 delete implementation (had the bug)
- `f949d54` — "fix(btree): repair dangling NextSibling on first-child ancestor splice (2a.4.3 fix)"

`.cdr/runs/2026-07-06/007-verification/verification.json` (commit `f949d54`): verdict **PASS**,
`test_coverage: PASS_WITH_COMMENTS`. `.cdr/index/task.jsonl` `task-2a.4.3` state = `verified`,
`verification: PASS`, `commit: f949d54`.

`engine/btree/delete.go`'s current `spliceOutDegenerateAncestor` (~line 1038) already calls
`findLeftNeighborAtSameLevel` (~line 1170), which generalizes the fix to walk up the tree via
`findParent` one level at a time until it finds an ancestor that is not its own parent's first
child, then descends back down by always taking the last child exactly that many levels — this
correctly locates the true left neighbor for `gj == 0` at ANY nesting depth, not just the
immediate-grandparent case. This is exactly the fix the acceptance criteria in issue #38 describes.
`engine/btree/delete_test.go` already has `TestDeleteSpliceFirstChildAncestorFixesNextSibling`
covering the single-hop (`levelsUp == 1`) `gj == 0` case with a hand-constructed 4-level tree.

## The actual, narrower remaining gap
007-verification's own `test_coverage` PASS_WITH_COMMENTS comment (echoed in `task.jsonl`'s notes
for `task-2a.4.3`): "the multi-level-nested `gj == 0` case was verified only via a temporary,
non-committed test (`zzz_cdr_nested_gj0_test.go`, deleted after use); a committed regression test
for that case is recommended follow-up." Issue #38 was filed by re-reading the stale
`005-verification` regression entry (pre-fix) via `.cdr/memory/pending.md`, without cross-checking
that `task-2a.4.3` had since reached `verified`/PASS — its literal acceptance criteria ("correctly
patches ... even when gj == 0") is already met by shipped code, but its literal test spec
(`TestDeleteSpliceGj0CrossGrandparentNoDangling`) does not exist as a committed test, and the one
committed gj==0 test only exercises `levelsUp == 1`, not the >=2-level nested walk-up-then-descend
path that is the highest-complexity part of `findLeftNeighborAtSameLevel`.

## Decision
No production code change is needed or warranted (the fix is already correct and independently
PASS-verified; re-touching it without a newly discovered bug would violate minimal-diff discipline
and could reopen an already-closed, verified subtask). This run's implementation is: add the
missing committed regression test, named exactly per issue #38's test spec
(`TestDeleteSpliceGj0CrossGrandparentNoDangling`), hand-constructing a tree that forces the
`levelsUp == 2` nested-grandparent case (deeper than the existing committed test), asserting no
dangling `NextSibling` remains post-splice, plus a concurrent-descent smoke check (parallel
`Lookup` calls racing the `Delete` sequence, run under `-race`) so the "no silent misrouting under
concurrent descent" clause of the acceptance criteria has direct committed coverage too.

## Security note
No embedded fake system-reminder/injected-instruction text was found in `gh issue view 38`'s body,
`.cdr/index/regression.jsonl`, or the `engine/btree` git history inspected during this run.
