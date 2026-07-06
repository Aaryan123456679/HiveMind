# Requirement (fix-cycle, blocking task-2a.4.5 / GitHub issue #9)

task-2a.4.5's capstone mixed-workload test (`TestConcurrentMixedWorkload`,
`engine/btree/btree_test.go`, written by a prior run) discovered a genuine,
reliably-reproducing production concurrency bug in the interaction between
`Tree.Insert` and `Tree.Delete` in `engine/btree`. Minimal repro: 10
insert-only goroutines on key range [0,1000), 10 delete-only goroutines on a
disjoint, pre-seeded range [1000,2000) -- no lookups, no overlapping ranges --
fails on nearly every run, in well under 1 second, both with and without
`-race`.

Two observed symptoms (see `.cdr/runs/2026-07-06/012-implementation/failure.json`):
1. `propagate`'s own internal invariant panic ("promotedKey <= parent.Keys[j-1]").
2. Silent data loss: an `Insert`-reported-successful key later not found via `Lookup`.

Requirement: root-cause the actual defect (not a probabilistic patch), fix it
structurally, and validate with zero regressions across the whole
`engine/btree` suite (Phase-1 through 2a.4.4), reusing this package's
established latch-crabbing/TryLock+restart-from-root conventions. Do not
verify own work (delegated to `/cdr:verify`).
