# Requirement

Fix the silent-data-loss regression recorded in
`.cdr/runs/2026-07-05/022-verification/verification.json` for subtask 2a.4.2
(GitHub issue #9, "Latch-crabbing B+Tree concurrency"). Re-verification of the
round-1 fix (commit `f0e972c`) confirmed that fix correctly resolved the
originally-reported `findParent` leaf-chain-routing hard error, but pushing
the adversarial harness harder (160 goroutines, 80,000 keys, vs. the shipped
test's 64 goroutines / 30,080 keys) found a distinct, more severe bug: a
previously-inserted key becomes unfindable via `Lookup` afterward, with no
error surfaced anywhere by `Insert` or elsewhere. Reproduced at ~8.6% (3/35
runs) at the larger scale; not reproduced at the shipped test's smaller
scale across 40 runs.

This run resumes an in-progress fix cycle (023-implementation-fix) that was
cut off mid-task by a session/API limit. On resuming, `engine/btree/insert.go`
already had an uncommitted, in-progress fix in `Tree.propagate` (sorting the
promoted key's insertion position via `sort.Search` instead of a stale
positional index `j+1`), and `engine/btree/zzrepro_test.go` (untracked, a
temporary 160-goroutine/80,000-key repro harness) already existed.

Task: validate the in-progress fix's stated root cause, confirm empirically
(not just by inspection) that it eliminates the silent-data-loss repro,
confirm no regression on the round-1 bug or any other existing test, decide
whether the committed test suite should be strengthened to catch this class
of bug at a committable scale, delete the temporary repro harness, write the
CDR fix artifacts, and make one commit. Verification is explicitly out of
scope here (delegated to `/cdr:verify`).
