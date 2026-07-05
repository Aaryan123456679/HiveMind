# Requirement

Fix the CHANGES_REQUESTED regression recorded in
`.cdr/runs/2026-07-05/020-verification/verification.json` for subtask 2a.4.2
(GitHub issue #9, "Latch-crabbing B+Tree concurrency"), commits `eff41a0` /
`edd81f9`.

Verifier findings being addressed:
- `requirements_conformance` / `blink_tree_fix_soundness_point1_4`: an
  adversarial harness (64 goroutines, ~30,000 keys, forcing tree depth >= 2)
  reproduced `btree: internal invariant violated: findParent reached leaf N
  while searching for the current parent of M along path ...` at roughly a
  1-in-20 to 1-in-25 rate under `-race`.
- `test_coverage_point6`: `assertStructuralInvariants` and
  `TestCrabbingInsert` never exercised internal-level `NextSibling`/`LowKey`
  correctness or forced tree depth >= 2, so this class of bug was
  undetectable by CI.

Task: reproduce the bug myself first, root-cause it (verifier offered two
candidate hypotheses but did not bisect to a confirmed cause), fix the root
cause, strengthen tests to cover the previously-blind regime, re-confirm the
fix against the original repro, run full validation, and commit once.
Verification of this fix is explicitly out of scope (delegated to
`/cdr:verify`).
