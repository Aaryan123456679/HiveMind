# Plan — 2b.1.2

1. Add `engine/split/guard.go`:
   - `fileSplitState` struct: single field `inProgress atomic.Bool`.
   - `FileGuard` struct: `mu sync.Mutex`, `guards map[uint64]*fileSplitState`.
   - `NewFileGuard() *FileGuard`: constructs with initialized map.
   - `stateFor(fileID uint64) *fileSplitState` (unexported, lazily get-or-create, mirrors
     `latchFor` idiom from `btree/latch.go`).
   - `TryAcquire(fileID uint64) bool`: `stateFor(fileID).inProgress.CompareAndSwap(false, true)`.
   - `Release(fileID uint64)`: `stateFor(fileID).inProgress.Store(false)`.
   - `InProgress(fileID uint64) bool`: `stateFor(fileID).inProgress.Load()` — read-only
     observability, does not mutate, useful for tests/logs (mirrors `Version` on nodeLatch).
   - Full doc comments matching repo's dense documentation convention (see trigger.go / latch.go
     style): explain CAS semantics, winner/loser contract, no-retry-loop expectation, Release
     no-op-if-not-held choice, no-eviction growth-characteristic note pointing at pending.md.

2. Add `engine/split/guard_test.go`:
   - `TestSplitInProgressCAS`: spin up N (e.g. 200) goroutines all calling `TryAcquire` on the
     SAME fileID concurrently (use a start barrier via a channel/WaitGroup so they actually race),
     count wins with `atomic.Int64`; assert exactly 1 win, N-1 losses. Run under `-race`.
   - `TestAcquireReleaseReacquire`: acquire -> true, second acquire before release -> false,
     release, acquire again -> true.
   - `TestIndependentFileIDs`: two different fileIDs each independently acquirable concurrently
     (guard on fileID A does not block/interfere with fileID B).
   - `TestReleaseWithoutHoldingIsNoOp`: call `Release` on a fresh/never-acquired fileID; assert no
     panic and a subsequent `TryAcquire` still succeeds (true) — documents the no-op design choice.
   - `TestInProgressObservability` (optional but cheap): `InProgress` reflects true after acquire,
     false after release, without itself being a race (`-race` covers this automatically since it's
     exercised alongside the CAS test's concurrent goroutines too, if convenient).

3. Update `.cdr/memory/pending.md` with the one-line no-eviction note for the new registry,
   matching precedent format of the existing btree entry.

4. Run self-consistency checks (build/vet/fmt, targeted race test x5, full split race suite,
   catalog + btree regression) per instructions before committing.

5. One local commit, Problem/Solution/Impact style matching `git log` convention (see 9214a83).

6. Write validation-matrix.json, self-consistency.json, handoff.json.
