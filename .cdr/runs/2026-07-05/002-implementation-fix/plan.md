# Fix plan: task-2a.2.2 TOCTOU race in NewSnapshot (issue #7 CHANGES_REQUESTED)

## Requirement
Per verification.json's central_question: does NewSnapshot's two-step "read
CurrentVersion, then acquire current epoch" sequence let a concurrent commit
interleave and cause RunCompaction to prematurely delete the exact version the
snapshot is pinned to? Verified: yes, deterministically. Fix must restore the
acceptance criterion "a version file is deleted only once its epoch's refcount
is zero" for the actual live snapshot.

## Root cause
`CommitVersion`'s successful path (write.go's `commitVersionWithHook`) does,
strictly in this real-time order:
1. `walCAS` returns `true` once `Catalog.CompareAndSwapCurrentVersion` has
   applied — CurrentVersion is now visible as the new version (say V+1).
2. `em.AdvanceEpoch()` — increments the global epoch counter, returning E'.
3. `vw.recordVersionEpoch(fileID, V+1, E')` — records that V (the old current)
   was superseded at epoch E'.

The buggy `NewSnapshot` did:
1. `cat.Get(fileID)` -> reads CurrentVersion = V.
2. `em.AcquireCurrentEpoch()` -> acquires whatever epoch is current NOW.

If a full CommitVersion (steps 1-3 above) completes for V's successor between
the snapshot's step 1 and step 2, the snapshot's acquired epoch is
`>= E'` (since AdvanceEpoch already ran) while the snapshot is still pinned to
the OLD version V. RunCompaction's skip condition is
`anyReferenced && minRef < supersededAtEpoch(V)`; with `supersededAtEpoch(V) ==
E'` and the snapshot's own acquired epoch `>= E'`, `minRef < E'` is false, so
RunCompaction proceeds to delete V's file out from under the live snapshot.

## Fix
Reorder `NewSnapshot` to call `em.AcquireCurrentEpoch()` FIRST, then
`cat.Get(fileID)` second. No retry/seqlock loop is needed — reordering alone
is provably sufficient. Proof:

Let `E0` = the epoch the snapshot acquires (read under `em.mu` at time
`T_acquire`). Let `V` = `rec.CurrentVersion` observed at time `T_read`
(`T_acquire < T_read`, since acquire now happens first).

Claim: for ANY version W that later supersedes V via a CommitVersion whose CAS
applies at time `T_cas`, the epoch `E'` recorded via `recordVersionEpoch` for
that supersession (i.e. `AdvanceEpoch()` called at time `T_advance >
T_cas`) satisfies `E' > E0`.

Proof: `cat.Get` returning `CurrentVersion == V` at `T_read` means the CAS that
moves CurrentVersion away from V has NOT yet applied at `T_read` (Catalog's
CAS is a linearizable, monotonically-forward compare-and-swap guarded by a
per-fileID lock — see catalog.go's `CompareAndSwapCurrentVersion` — so once V
is observed, no earlier CAS could have already superseded it and no later
Get could revert to V). Therefore `T_cas > T_read`. Since `T_advance > T_cas`
(AdvanceEpoch is called strictly after the CAS applies, per
commitVersionWithHook's code, lines 274-280), we have
`T_advance > T_cas > T_read > T_acquire`.

`AcquireCurrentEpoch` and `AdvanceEpoch` are both critical sections under the
SAME mutex (`em.mu`), and `T_acquire` strictly precedes `T_advance` in real
time with no overlap possible (there's slack — `T_read` and `T_cas` occur
in between on two different goroutines, but crucially neither of those two
calls touches `em.mu`, so they cannot reorder the two `em.mu` critical
sections relative to each other). Because `em.mu` gives these two calls a
definite linearization order matching their real-time order, `AcquireCurrentEpoch`'s
critical section (which reads `em.current == E0`) fully completes before
`AdvanceEpoch`'s critical section (which computes `E' = em.current(then) + 1`)
begins. Hence at the moment `AdvanceEpoch` runs, `em.current >= E0` (it can
only have grown from other, unrelated commits in between), so
`E' = em.current + 1 >= E0 + 1 > E0`. QED.

Therefore `supersededAtEpoch(V) = E' > E0` ALWAYS, for every possible
interleaving. RunCompaction's skip condition `minRef < supersededAtEpoch(V)`
is guaranteed true as long as this snapshot's `E0` is (or is the minimum of)
the currently-referenced epochs — i.e. `minRef <= E0 < E' = supersededAtEpoch(V)`
— so RunCompaction can never delete V while this snapshot is open. This holds
regardless of whether the snapshot's later `cat.Get` observes V (old) or
V+1/newer (new) — in the "observes newer" case the snapshot is simply pinned
to a version that hasn't been superseded yet from its own perspective, which
is trivially safe (never reclaimed while still current).

This is a pure reordering fix — no change needed to CommitVersion's existing
CAS-then-AdvanceEpoch-then-record sequence, and no seqlock/retry loop in
NewSnapshot is required.

## Changes
1. `engine/mvcc/read.go`: swap `AcquireCurrentEpoch()`/`cat.Get()` order in
   `NewSnapshot`; rewrite the "Epoch wiring" and "Race note" doc comments to
   describe the corrected order and the proof above (replacing the incorrect
   "delayed, never premature" claim, which was actually about the wrong
   ordering).
2. `engine/mvcc/read_test.go`: add `TestNewSnapshotAcquiresEpochBeforeReadingVersion`
   (or similar name in gc_test.go) — a hook-based regression test that pauses
   NewSnapshot between its two (now reordered) steps, lets a concurrent
   CommitVersion + RunCompaction run to completion in that gap, resumes, and
   asserts the snapshot's pinned version file still exists and reads
   correctly.
3. No changes to gc.go or write.go — their existing logic and doc comments
   already describe the correct invariant; only NewSnapshot's use of them was
   wrong.

## Validation matrix
- Existing: TestEpochRefcount, TestEpochRefcountConcurrent, TestCompactor,
  TestConcurrentReadersWriters, TestSnapshotRead,
  TestSnapshotReadNoVersionCommitted, TestVersionWriter, TestCurrentVersionCAS,
  TestVersionCASWAL — must all still pass unmodified.
- New: regression test proving the exact race from verification.json is now
  closed (version file still present/readable after concurrent commit+compact
  interleaves with NewSnapshot).
- `-race -count=10` on the new test and TestCompactor for flakiness.
- Full `go build ./...`, `go vet ./...`, `gofmt -l`, and `go test ./... -race
  -count=1` for the whole engine module.
