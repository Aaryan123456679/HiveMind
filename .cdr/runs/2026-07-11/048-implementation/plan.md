# Plan — 4.5.3.2

1. `engine/split/guard.go`:
   - Add `refs int` to `fileSplitState`, documented as guarded entirely by `FileGuard.mu` (never
     atomically), mirroring `nodeLatch.refs`.
   - Replace the non-refcounted `stateFor` with:
     - `getOrCreateLocked(fileID)` — caller must hold `mu`; get-or-create only, no refs touch.
     - `acquireGuard(fileID) *fileSplitState` — lock `mu`, get-or-create, `refs++`, return.
     - `releaseGuard(fileID, s *fileSplitState)` — lock `mu`, `refs--`; evict iff `refs == 0 &&
       !s.inProgress.Load()` (checked `cur == s` defense-in-depth, same as btree).
   - `TryAcquire`: `s := acquireGuard(fileID)`; if `s.inProgress.CompareAndSwap(false, true)` ->
     return true (ref stays open, closed later by `Release`); else `releaseGuard(fileID, s)` ->
     return false (mirrors `TryLock`'s failure path).
   - `Release`: plain locked map lookup (NOT a panicking peek — must stay a documented no-op on
     miss per existing tests); if found: `s.inProgress.Store(false)` FIRST, then
     `releaseGuard(fileID, s)` (order is load-bearing, see architecture-discovery.md). If not
     found: no-op, return.
   - `InProgress`: `s := acquireGuard(fileID)`; `v := s.inProgress.Load()`;
     `releaseGuard(fileID, s)`; return `v` (transient pin, mirrors `Version`).
   - Add `guardRegistrySize() int` test-only helper (mirrors `latchRegistrySize`).
   - Update the struct-level doc comment: replace the "Growth characteristic: ... never evicted...
     deliberate, deferred" paragraph with a paragraph describing the new bounded behavior, the
     `refs==0 && !inProgress` gate, why no version-like history needs preserving (flag has no
     accumulating history, unlike btree's version counter), and the store-before-evict ordering
     rationale.
2. `engine/split/guard_test.go`: add `TestFileGuardRegistryBounded` — guard a large number
   (>= 10,000) of distinct fileIDs, `TryAcquire` then `Release` each (simulating real winner
   lifecycle), and assert `guardRegistrySize()` stays small (well under the total number of
   distinct fileIDs used), proving the registry doesn't grow unboundedly. Also include a check
   that a fileID left in-progress (no Release) is NOT evicted, exercising the gate directly.
3. Self-consistency: `go test ./engine/split/... -race -v -count=3` (scoped only to
   `engine/split/...` per launch instructions; do not touch other packages' concurrent work).
4. One local commit, explicit file paths only, `.cdr/runs/2026-07-11/048-implementation/...` docs
   and `engine/split/guard.go` + `engine/split/guard_test.go`.
5. `handoff.json` with pointers only, plus concurrency-safety reasoning matching 4.5.1.3's
   handoff.
