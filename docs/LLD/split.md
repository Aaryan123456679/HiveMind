---
last_synced_commit: ee033192ecc70c76aaa116f610c52bcdaaaa0462
---

# LLD: `engine/split/`

Status: implemented (`engine/split/orchestrate.go`, `execute.go`, `trigger.go`, `guard.go`,
`proposer*.go`, wired into production via `engine/catalog/content.go` and
`engine/cmd/smokeserver/main.go`). Only `engine/split/doc.go` itself is a placeholder (the
package-level doc comment file). See [HLD.md](../HLD.md) for system context.

**This is the highest-risk correctness surface in the entire engine.** Any change here needs a
dedicated concurrent race test before being considered done — see "Known risks" below.

## Purpose

Automatically splits a topic file into multiple topic-coherent files once it grows too large,
keeping topics well-scoped as the corpus grows.

## Trigger

`split.Trigger` (`trigger.go`) is a stateless size-threshold detector: `Trigger.Detect`/the
standalone `CrossesThreshold` predicate evaluate a single append's before/after content size
against a configured threshold (default `DefaultThresholdBytes` = ~8KB / ~2000 tokens), firing a
`Signal` exactly once per crossing (never re-signaling a file already over threshold, never
signaling a shrinking or non-growing append).

Wiring into the live write path (`ContentStore.Append`, `engine/catalog/content.go`) is via
dependency inversion, not a direct import: `engine/catalog` cannot import `engine/split`
directly (`engine/split` already imports `engine/catalog` for `CatalogRecord`/status types, so
the reverse import would be circular). Instead, `ContentStore` exposes a `SplitTriggerFunc`
hook (`func(fileID, oldSizeBytes, newSizeBytes uint64) bool`) installed via
`ContentStore.SetSplitTrigger`; a composition root that imports both packages —
`engine/cmd/smokeserver/main.go` — constructs a real `*split.Trigger` and installs an adapter
closure (`trig.Detect(fileID, old, new)` mapped to the `bool` the hook returns). A nil
`SplitTriggerFunc` (the default for callers/tests that never call `SetSplitTrigger`) falls back
to `ContentStore`'s own inline `splitThresholdBytes` comparison. This is what makes a threshold
crossing actually surface a split signal in the compiled production binary, not just in
`engine/split/trigger_test.go`.

## Split sequence

1. Mark the file `SPLITTING` in the [catalog](catalog.md) via `split.Orchestrator.BeginSplit`
   (see "Concurrency control" below); new writers to this specific file are queued (refused with
   `ErrSplitInProgress` via `Orchestrator.AdmitWrite`, expected to back off and retry). Existing
   readers are **not** given true MVCC/snapshot isolation here: `catalog.ContentStore.Read`
   reads the file's content bytes directly off disk, gated only by the catalog lookup
   succeeding, never by `CatalogRecord.Status`. The real guarantee is narrower but still load-
   bearing — `ContentStore`'s write-to-temp-then-atomic-rename technique
   (`writeContentFile`/`writeNewContentFile`) means a concurrent `Read` during an in-flight split
   always observes either the fully-consistent pre-split content or the fully-consistent
   post-split (redirect-stub) content, **never a torn/partial byte sequence** — but it is not
   repeatable-read/version-pinned: two `Read` calls issued moments apart during the window can
   legitimately return different results as the split lands. True snapshot isolation would
   require wiring this path through [`engine/mvcc`](mvcc.md)'s `Snapshot`/`CurrentVersion`
   machinery, which `ExecuteSplitAtomic` deliberately does not touch (it mutates
   `Status`/`RedirectTargetIDs`/`SizeBytes` only) — tracked as separate, future work, not part of
   this package's current guarantee. This is exactly what
   `engine/split/split_race_test.go`'s `TestReaderDuringSplit` exercises and proves against the
   real `ContentStore`/`ExecuteSplitAtomic` pair (an earlier version of this test pinned an
   `mvcc.Snapshot` against a separate root untouched by the real split path and was a tautology
   that could not have caught a broken concurrency guarantee — see that test's doc comment for
   the history).
2. Call the Python ingestion agent's `ProposeSplit(fileContent)` RPC (see
   [ingestion-agent.md](ingestion-agent.md)) for a topic-coherent split plan:
   `[{newPath, sectionRanges}, ...]` plus a redirect summary.
3. Atomically (`split.ExecuteSplitAtomic`, `execute.go`):
   - Allocate new `fileID`s.
   - Write the new `.md` files.
   - Write a redirect/stub at the old path (reusing the original `fileID` — only its content and
     catalog Status/RedirectTargetIDs change, not its identity).
   - Update catalog entries for all affected files.
   - Add `SPLIT_SIBLING` graph edges between the new files (see [graph.md](graph.md)), via
     `graph.EdgeAppender.AppendEdgeIfAbsent` (idempotent, safe to replay).
   - Re-point or leave inbound edges pointing at the redirect stub — deliberately simpler than
     rewriting a potentially large inbound-edge list.
4. Commit as a single WAL-covered transaction. The actual point of no return is the single
   `wal.RecordSplitCommit` append/fsync: before it, nothing durable or catalog/B+Tree/graph-
   visible has happened (new-file/stub content may already be on disk but is unreferenced,
   harmless garbage if a crash happens here); after it durably lands, the split's full intended
   effect is guaranteed reachable via `RecoverSplitCommits` replaying `cat.Put` + every B+Tree
   insert + every graph edge append, regardless of how much of the original in-memory apply had
   run before a crash. A test-only synchronous stage-callback seam, `atomicCommitHook` (nil/
   no-op in production), lets tests deterministically simulate a crash or barrier at named
   stages (`"before_commit_append"`, `"after_commit_before_catalog_put"`, etc.) — this same seam
   is what backs `TestReaderDuringSplit`'s deterministic reader/split synchronization (see
   "Known risks" below), not just crash-injection.

## Concurrency control

`FileGuard` (`guard.go`) is a per-`fileID`, reference-counted, bounded registry of
`fileSplitState{inProgress atomic.Bool, refs int}` entries: `FileGuard.TryAcquire`/`Release`
implements a CAS-based "exactly one caller wins the right to split this file" gate, ensuring
exactly one split wins per threshold crossing even when many concurrent writers/triggers race
for the same file. The registry is bounded via reference-counted eviction (mirroring
`engine/btree`'s `NodeStore` latch-registry eviction) rather than growing unboundedly with every
distinct `fileID` ever guarded.

`split.Orchestrator` (`orchestrate.go`) composes `FileGuard` with the catalog's `Status` field
(`StatusActive` -> `StatusSplitting` -> `StatusSplit`/`StatusRedirect`) via three primitives:
- `BeginSplit(fileID)` — wins `FileGuard.TryAcquire`, then durably transitions
  `Status: Active -> Splitting` (refusing with `ErrAlreadySplitting` and releasing the guard on
  any failure after winning it).
- `EndSplit(fileID, outcome)` / `AbortSplit(fileID)` — durably transitions back out of
  `Splitting` to `outcome` (`StatusSplit` on success, `StatusActive` on abort) and always
  releases the guard, whether the transition itself succeeds or fails.
- `AdmitWrite(fileID)` — the write-admission gate: refuses with `ErrSplitInProgress` exactly
  when `Status == StatusSplitting`, otherwise admits (including already-`Split`/`Redirect`
  records, whose writer semantics are this package's execution logic's concern, not
  `AdmitWrite`'s).

## Interactions with other modules

- `catalog/` — status transitions (`ACTIVE` -> `SPLITTING` -> `SPLIT`/`REDIRECT`), new records for
  split-off files.
- `mvcc/` — not currently wired into the split path at all (see "Split sequence" step 1 above);
  `mvcc.Snapshot`/`NewSnapshot`/`Read` pin a `fileID`'s `CurrentVersion`, which `ExecuteSplitAtomic`
  never touches, so any in-flight `mvcc.Snapshot` is structurally unaffected by a concurrent
  split — but this is a separate, narrower fact than "readers get MVCC-mediated isolation
  during a split," which they do not.
- `btree/` — new topic paths inserted, old path repointed to a redirect stub.
- `graph/` — `SPLIT_SIBLING` edges added between split-off files; inbound edges retargeted to the
  redirect stub rather than rewritten en masse.
- `wal/` — the entire split is one WAL-covered, fsynced transaction.
- `agents/ingestion/` — `ProposeSplit` RPC supplies the actual split plan; the engine only
  executes it.

## Known risks

- **Auto-split correctness under concurrency**: needs a dedicated concurrent race test — many
  goroutines appending to the same file simultaneously — asserting: no data loss, exactly one
  split per threshold crossing, and no dangling graph edges. Must run under `go test -race` per
  the engine-wide convention in [AGENT.md](../../AGENT.md).
- **Section-index staleness**: the markdown header-offset cache used for `ReadPartial` must be
  invalidated atomically within the same split transaction that rewrites file boundaries.
- **Abandoned-`SPLITTING` lease-reclaim: crashed-holder guard leak (accepted, disclosed
  limitation)**: `Orchestrator` records a lease (`leaseEntry{deadline, gen, reclaimed}`) each
  time `BeginSplit` wins; if a later `BeginSplit` for the same `fileID` loses `TryAcquire`, it
  calls `reclaimIfExpired`, which force-reverts a lease-expired record from `Splitting` back to
  `Active` (unblocking `AdmitWrite` callers) — but, since a fix-cycle correction, **no longer
  releases the `FileGuard` hold itself**. Earlier it did, on the theory that an expired lease
  implies it's safe to let a new caller start over; that was found unsafe: `FileGuard.Release`
  has no notion of caller identity/fencing, so releasing it purely on a timeout judgment could
  let a merely-slow (not actually crashed) original holder and a freshly-started second caller
  both believe they are the sole valid split-executor for the same `fileID` simultaneously.
  The accepted tradeoff: a *genuinely crashed* holder's guard is never released by anyone, so
  future `BeginSplit` calls for that `fileID` keep returning `ErrAlreadySplitting` until process
  restart — only the *writer*-blocking-forever half of the original gap is fixed
  unconditionally (via `AdmitWrite` consulting catalog `Status`, which `reclaimIfExpired` does
  revert, independent of the guard's stuck state). Closing this fully would require
  `FileGuard.TryAcquire`/`Release` to thread an ownership/fencing token, which is out of this
  package's current scope.
- **`AdmitWrite` checks catalog `Status` only, never the guard (open, currently inert
  finding)**: `AdmitWrite` is a point-in-time `Status` read, not a CAS against `FileGuard`; it
  does not make "check status, then write" atomic end-to-end against a concurrent `BeginSplit`.
  This is harmless today because no real writer path in this codebase calls `AdmitWrite` yet
  (it exists as the entry gate for a future writer integration), but it is a real gap to close
  before wiring a genuine writer path through it.

## Cross-references

- [HLD.md](../HLD.md)
- [catalog.md](catalog.md), [mvcc.md](mvcc.md), [btree.md](btree.md), [graph.md](graph.md),
  [wal.md](wal.md)
- [ingestion-agent.md](ingestion-agent.md) — `ProposeSplit` RPC implementation
