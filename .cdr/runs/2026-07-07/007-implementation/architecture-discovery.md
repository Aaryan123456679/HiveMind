# Architecture discovery — subtask 2b.1.3

## Files read (read-only dependencies, not modified)
- `engine/split/trigger.go` — stateless size-threshold `Trigger`/`Signal`/`Detect`.
  No per-file memory; callers pass (old,new) sizes per append. Not touched.
- `engine/split/guard.go` — `FileGuard` per-file CAS (`atomic.Bool` via
  `CompareAndSwap`) keyed by fileID, lazily created under a package-level
  `sync.Mutex` guarding only map access (idiom shared with
  `engine/btree/latch.go`'s `NodeStore`). `TryAcquire`/`Release`/`InProgress`.
  Documented contract: winner of `TryAcquire` must eventually call `Release`
  once the split "completes (success or failure)"; 2b.1.2 explicitly defers
  "the actual back-off/queueing/status-transition behavior for losers" to
  2b.1.3. Not touched.
- `engine/catalog/record.go` — `CatalogRecord` already has a `Status
  RecordStatus` field with `StatusActive/StatusSplitting/StatusSplit/
  StatusRedirect` constants and `RedirectTargetIDs []uint64` — i.e. the
  on-disk data model for this subtask's status transition already exists
  (added in an earlier subtask, unused until now). No schema change needed.
- `engine/catalog/catalog.go` — `Catalog.Put/Get/Delete` (per-fileID stripe
  lock + index map), plus the existing
  `CompareAndSwapCurrentVersion(fileID, expected, newVersion)` CAS primitive
  used by `engine/mvcc`'s `CommitVersion`. This is the established idiom for
  "safely mutate one field of a CatalogRecord under its own stripe lock,
  refusing if a precondition no longer holds" — orchestrate.go's SPLITTING
  transition follows the same shape (read-check-write under `Get`, guarded by
  the fileID's own stripe lock via `Catalog.Get`/`Put`), rather than adding a
  new dedicated CAS method to `Catalog` (out of scope: `catalog.go` is not an
  impacted module for 2b.1.3, and a read-then-conditional-Put sequence,
  serialized externally by `FileGuard`'s per-fileID exclusivity, is sufficient
  here — see "Concurrency correctness" below for why no TOCTOU gap exists).
- `engine/catalog/content.go` — `ContentStore.Append`'s WAL-before-apply
  pattern (`wal.NewCatalogPutRecord` + `wal.AppendAndApply`, only then
  `cat.Put`) is the established idiom for "any catalog mutation must be
  logged to the WAL before it is applied" (per `docs/LLD/wal.md`). This
  subtask's SPLITTING/exit-SPLITTING transitions reuse exactly this idiom
  rather than calling `cat.Put` directly, so the Status transition itself is
  crash-durable and WAL-replay-safe on the same terms as every other catalog
  mutation in this codebase. `ContentStore.Append` itself is NOT modified
  (out of scope for 2b.1.3 — wiring an admission check into the live append
  path is left for whichever later subtask actually connects `engine/split`
  to `engine/catalog`'s write path; issue #10's impacted-modules list for
  2b.1.3 is `engine/split/orchestrate.go` + its test only).
- `engine/mvcc/write.go` (`VersionWriter.CommitVersion`/`walCAS`) and
  `engine/mvcc/read.go` (`Snapshot`/`NewSnapshot`/`Read`/`SnapshotRead`) — the
  actual MVCC snapshot-isolation machinery this subtask must show is
  unaffected by a SPLITTING transition. Key facts:
  - `NewSnapshot` pins a fileID's `CurrentVersion` (read via `cat.Get`) plus
    an epoch reference; `Snapshot.Read` then reads the immutable
    `<fileID>.v<CurrentVersion>.md` file. Neither step reads or depends on
    `CatalogRecord.Status` at all.
  - Version files are immutable once written (`WriteVersion` always assigns a
    brand-new, never-reused version number) and are never deleted except by
    the epoch-refcounted background compactor (`gc.go`), which only reclaims
    versions no in-flight `Snapshot` still references.
  - Therefore: a `CatalogRecord.Status` transition (Active -> Splitting ->
    Active/Split) is **structurally orthogonal** to `CurrentVersion` and to
    on-disk version-file bytes. A `Snapshot` taken before, during, or after a
    SPLITTING transition keeps reading whatever `CurrentVersion` it pinned at
    `NewSnapshot` time, byte-for-byte, regardless of `Status`'s value at any
    point in that window — *as long as nothing else concurrently advances
    CurrentVersion while SPLITTING* (true here: 2b.1.3 does not call
    `CommitVersion`, and this subtask's own write-admission gate is precisely
    what prevents ordinary writers from doing so while SPLITTING).

## Scope boundary vs issue #12
Confirmed via `gh issue view 12`: 2b.3.1-2b.3.6 own fileID allocation, new
content files, redirect stub + `StatusSplit`/`StatusRedirect` catalog updates
with populated `RedirectTargetIDs`, B+Tree repointing, graph edges, and the
single atomic WAL-covered commit that "releases queued writers on commit".
2b.1.3 (this subtask) is explicitly the *entry* into SPLITTING plus the
write-admission gate that makes "queued rather than applied" true during the
window before #12's execution logic runs; #12 owns the *exit* transition's
real content (RedirectTargetIDs, stub files, etc.) and the atomic commit that
ultimately un-gates writers for real. This subtask's `EndSplit` is a generic,
outcome-parameterized primitive (back to `StatusActive` on abort, forward to
`StatusSplit` on success) that #12's execution logic is expected to call once
it has actually finished its own work — 2b.1.3 does not attempt to duplicate
#12's atomic-commit machinery, and does not populate `RedirectTargetIDs`
itself (that requires data #12 alone produces).

## "Queue new writers" — concrete design choice
This codebase's established idiom for "someone else is working on this,
caller must back off/retry" is `engine/btree`'s `TryLock`-miss +
restart-from-root pattern (`btree/delete.go`), and `FileGuard.TryAcquire`
itself (loser gets `false`, is documented to back off, not retry-loop
waiting for the flag to clear). There is no blocking channel/condvar
primitive anywhere in this codebase for this class of problem. Following
that precedent, "queueing" a new writer here means: the writer's call is
answered immediately with a distinguishable sentinel error
(`ErrSplitInProgress`), and the *documented contract* is that the writer is
expected to back off and retry later (mirroring `TryLock`'s
restart-from-root contract) rather than being silently applied or silently
dropped. This subtask provides that gate (`AdmitWrite`) as a check any writer
path is expected to call before mutating a file's content; it does not
itself modify `ContentStore.Append` to call it (out of scope, per the
impacted-modules list), and does not invent a new blocking primitive.

## Concurrency correctness reasoning
- `BeginSplit(fileID)`: first calls `FileGuard.TryAcquire(fileID)`. Only the
  CAS winner proceeds past this point for a given fileID — this is exactly
  the same exclusivity 2b.1.2 already proved race-free
  (`TestSplitInProgressCAS`), reused here (not reimplemented) to guarantee at
  most one goroutine is inside the read-check-write status-transition
  sequence below for a given fileID at a time. This closes the TOCTOU window
  that would otherwise exist between `cat.Get` (read `Status`) and `cat.Put`
  (write `StatusSplitting`) if two goroutines could reach it concurrently.
- If the winning goroutine's status-transition sequence itself fails partway
  (record not found, encode error, WAL append error), `BeginSplit` releases
  the guard before returning the error, so the guard never leaks in a
  never-actually-entered-SPLITTING state.
- `EndSplit(fileID, outcome)` re-reads the record and requires
  `Status == StatusSplitting` before writing `outcome`, refusing (without
  panicking or corrupting state) if some other actor already moved it out of
  SPLITTING — then unconditionally releases the guard in all cases (success
  or refusal), since `FileGuard.Release` is documented as an idempotent
  no-op and the guard's job ends with "this actor's split attempt is over",
  matching `guard.go`'s "winner ... calls Release ... once the split
  completes (success or failure)" contract.
- `AdmitWrite(fileID)` is a point-in-time check (`cat.Get` + status
  comparison), not a CAS: it deliberately does NOT claim to make a
  write+status-check atomic end-to-end (that atomicity is #12's WAL-covered
  commit's job for the real split-completion path). For 2b.1.3's actual
  scope — demonstrating that a writer is refused while SPLITTING and admitted
  otherwise — a snapshot-style read check is sufficient and matches this
  subtask's boundary (an entry gate, not the full write pipeline).

## Crash/stuck-SPLITTING gap (explicitly out of scope, flagged not fixed)
If the process that won `BeginSplit` crashes/panics before calling `EndSplit`,
`StatusSplitting` is left durably on disk (already WAL-logged) with no
in-memory `FileGuard` entry surviving the crash (guard state is
process-lifetime-scoped, same as `Catalog`'s in-memory index — see
`catalog.go`'s own documented "empty index on load" gap). A fresh process
restarting would see `StatusSplitting` in the catalog but an unset
(zero-value, i.e. "not in progress") `FileGuard` entry, so a *new*
`BeginSplit` call could wrongly re-win the guard against an already-
SPLITTING record. This subtask's `BeginSplit` guards against exactly that:
it checks `Status == StatusActive` before transitioning (see implementation),
so a restart-after-crash scenario is caught as "refuses to begin a second
split over an already-SPLITTING record" rather than silently allowing a
double-split — but it does NOT provide automatic recovery/timeout back to
Active for a genuinely abandoned SPLITTING record left by a crashed holder.
That recovery story (plausibly tied to WAL replay / a restart-time catalog
scan) is a real, currently-open gap, consistent with several other
already-documented process-lifetime-scoped gaps in this codebase
(`catalog.go`'s index-rebuild gap, `idalloc`'s sidecar cross-check gap); it
is out of scope for 2b.1.3 (not listed in its impacted modules, and issue
#12's atomic-commit/crash-injection test (`TestSplitAtomicCommit`) is the
much more natural place recovery semantics for an in-flight split belong).
Recorded here for `.cdr/memory/pending.md` follow-up, not silently dropped.
