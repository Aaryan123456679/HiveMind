# Architecture discovery

## Read directly (source of truth, not the prose above)

- `engine/split/orchestrate.go` (full file, ~508 lines): `Orchestrator` struct, `leases`
  map (`map[uint64]time.Time` pre-fix), `recordLease`/`clearLease`/`reclaimIfExpired`,
  `BeginSplit`/`finishBeginSplit`/`EndSplit`/`AbortSplit`, `transitionStatus` (WAL-before-
  apply catalog Status CAS-by-convention, documented as relying on FileGuard-provided
  external serialization per fileID).
- `engine/split/guard.go` (full file, ~302 lines): `FileGuard`/`fileSplitState`.
  `TryAcquire` is a genuine CAS (`atomic.Bool.CompareAndSwap`). `Release` is a plain,
  non-panicking, **identity-free** flag clear: it looks up whatever `fileSplitState` is
  currently registered for fileID and sets `inProgress` false, with zero concept of "which
  caller is entitled to release this." This is the structural root cause the verification
  finding points at, and is confirmed by reading the code, not just the prose: there is no
  token, generation, or owner field anywhere in `fileSplitState` or `FileGuard`.
- `engine/split/split_race_test.go`: calls `orch.BeginSplit(fid)` / `orch.AbortSplit(fid)`
  with today's exact signatures. This file is **not** in this fix-cycle's editable scope,
  which makes any signature change to `BeginSplit`/`EndSplit`/`AbortSplit` a hard compile-time
  blocker, not just a scope-discipline concern.
- `engine/split/execute.go`: only *references* `Orchestrator.BeginSplit`/`EndSplit` in
  comments/documentation; contains no actual call sites. Confirms no other package currently
  has a compile-time dependency on these signatures besides the test file above.
- `engine/catalog/record.go`: `CatalogRecord` has `CurrentVersion` (MVCC content version,
  unrelated semantics) and `Status`, but no generation/fencing field of its own -- confirms
  no existing schema hook to piggyback a fencing token on without a catalog-package change
  (also out of this fix-cycle's scope).

## Key finding: full fencing is structurally blocked from two of three standard avenues

A textbook fencing-token fix (Kleppmann-style) needs the token to be verified at the point
where the *effect* happens. Here that's one of:
1. **FileGuard.TryAcquire/Release accepting/returning an ownership token.** Would require
   editing `engine/split/guard.go` -- explicitly out of scope; the task instructs to STOP
   and report rather than touch it.
2. **BeginSplit/EndSplit signatures threading a token from caller to caller.** Blocked by
   `engine/split/split_race_test.go`'s existing calls with the current fileID-only
   signatures (untouchable, and doing so would break compilation of the package).
3. **A generation/fencing check entirely internal to Orchestrator's own bookkeeping
   (`leases` map), with the mutating actions serialized so no two callers' catalog
   read-then-write sequences for the same fileID can interleave.** This is what's actually
   achievable within the stated scope, and is what this run implements.

## Consequence for the design

Avenue 3 can fully close:
- The TOCTOU window inside `reclaimIfExpired` itself and its interleaving with a concurrent
  `EndSplit` for the *same* fileID (both now hold `o.mu` across their own `transitionStatus`
  call, so they can never race each other's catalog Get/Put).
- The specific corruption chain: "H aborts/completes legitimately, C begins a fresh
  (different-generation) attempt, then a stale reclaim decision from before H's completion
  fires and wrongly reverts/clobbers C's fresh attempt" -- eliminated because reclaim no
  longer touches the guard at all (see below), so there is no "fresh C attempt" for a stale
  reclaim to clobber in the first place.

Avenue 3 CANNOT close (without avenue 1 or 2):
- "H is genuinely alive but slow (not crashed), reclaim fires (correctly, per the lease
  contract), a fresh caller C is allowed to become the guard's new sole holder, and H's own
  *eventual* EndSplit call -- which carries no identity/generation proof -- later succeeds
  against C's now-current Splitting status." `EndSplit(fileID)` has no way to distinguish
  "I am the rightful owner of the current generation" from "I am a stale holder from a
  reclaimed generation" using only `fileID`.

## Design decision

Rather than accept that unclosable gap silently, `reclaimIfExpired` is redesigned to **never
release the guard**. It only ever force-reverts the catalog `Status` (Splitting -> Active),
which is sufficient to unblock `AdmitWrite` (writers), because `AdmitWrite` only consults
`CatalogRecord.Status`, never the guard. The guard itself is only ever released by the true
holder's own (however belated) `EndSplit`/`AbortSplit` call, exactly as `FileGuard`'s own
documented "winner calls Release" contract already requires. This makes "two goroutines
simultaneously believing they are the sole valid split-executor for the same fileID"
**structurally impossible** (not merely less likely), at the cost of reintroducing part of
the *original*, pre-4.5.3.3 "future BeginSplit blocked forever" gap for a fileID whose
holder has *genuinely crashed* (as opposed to merely being slow) -- since nothing ever calls
`EndSplit` for such a fileID, the guard is never released. This tradeoff, and the two blocked
avenues that would be needed to close it without reopening the double-acquisition hole, are
documented in `orchestrate.go`'s package doc comment and in the handoff below.
