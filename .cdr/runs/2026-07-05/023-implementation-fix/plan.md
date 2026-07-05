# Plan

1. Read `git status`/`git diff engine/btree/insert.go` to see the exact
   in-progress, uncommitted state left by the interrupted prior agent run,
   rather than trusting the task prompt's paraphrase.
2. Read `Tree.propagate` in full context (not just the diff hunk) plus
   `findParent`'s leaf-chain-walk and per-parent lock/retry loop, to assess
   whether the round-2 fix's stated root cause (stale positional insertion
   index vs. `sort.Search`-based sorted position) is genuinely correct and
   sufficient, and whether the "`pos < j` should be unreachable" defensive
   fallback is actually reachable.
3. Empirically validate:
   a. Run the existing `TestZZReproSilentDataLoss` harness repeatedly with
      the in-progress fix applied.
   b. Run a counterfactual: revert `insert.go` to the original (round-1
      only) code via `git stash` and re-run the same harness to confirm it
      still reproduces the bug at the expected rate, validating that the
      test and diagnosis are both real (not artifacts).
   c. Restore the fix and re-run again to reconfirm.
4. Harden the defensive `pos < j` fallback to fail loudly (return a distinct
   invariant-violation error) instead of silently falling back to the old
   buggy behavior, since a silent fallback would defeat the point of a
   defensive guard if its precondition were ever violated.
5. Re-run the round-1 bug's original repro scenario
   (`TestCrabbingInsert/DeepOverlappingSubtree`) repeatedly to confirm no
   regression.
6. Run `go build`, `go vet`, `gofmt -l` and the full
   `go test ./engine/btree/... -race -v -count=1` and
   `go test ./... -race -count=1` (whole engine module) for zero
   regressions.
7. Decide whether `TestCrabbingInsert` needs a further-strengthened subtest
   to catch this class of bug at a committable scale, balancing runtime vs.
   reliability; add `VeryDeepOverlappingSubtree` (160g/80k, mirroring the
   temporary harness but using the existing `assertAllLookupable`/
   `assertStructuralInvariants` helpers) with the tradeoff explicitly
   documented in its doc comment (this scale only reproduces at ~8.6% per
   run, so a single CI run is not a fully reliable single-shot regression
   guard, but it is the smallest scale that reproduces the bug at all).
8. Delete `engine/btree/zzrepro_test.go` (temporary harness, not meant to
   be committed per its own header comment).
9. Re-run full validation once more with the final state (fix + hardened
   fallback + new subtest + harness deleted) to confirm everything is still
   green.
10. Write CDR fix artifacts (`requirement.md`, `root-cause.md`, `plan.md`,
    `self-consistency.json`, `handoff.json`), update
    `.cdr/index/task.jsonl`'s `task-2a.4.2` entry, leave the existing
    uncommitted `022-verification` entry in `.cdr/index/regression.jsonl`
    as-is, and make one local commit (no push).
