# Architecture discovery — 2b.3.6

## Existing primitives reused
- `wal.AppendAndApply(w, record, applyFn)`: structural fsync-before-apply
  guarantee already used for catalog/btree mutations. This is the mechanism
  reused verbatim for the split commit — no new fsync path invented.
- `wal.TypedRecord` / `RecordType` / `wal.Replay`: typed record + replay
  machinery. `engine/graph`'s append-only edge log deliberately bypasses
  this layer (byte-durable only, not replay-integrated) — this is exactly
  the gap flagged in `.cdr/memory/pending.md`.
- Multi-owner recovery precedent: `catalog.RecoverFromWAL` explicitly skips
  record types it doesn't own (documented behavior). This licenses adding a
  new record type owned exclusively by a new recovery pass, without
  disturbing existing recovery passes.
- Idempotent upserts: `catalog.Catalog.Put` and `*btree.Tree.Insert` are
  documented upserts already. `graph.EdgeAppender.AppendEdge` is NOT
  idempotent (pure append log) — this was the one place a new primitive
  was required for safe replay: `AppendEdgeIfAbsent`.
- Deterministic test-hook idiom: `engine/btree/lookup.go`'s
  `optimisticReadHook`, `engine/btree/insert.go`'s `crabRetryHook` — a
  nil-in-production, test-settable package-level func var. Mirrored as
  `atomicCommitHook func(stage string) error` in `engine/split/execute.go`.

## Design decision: one new WAL record type
Added `wal.RecordSplitCommit`, a single self-contained record encoding the
split's full effect: original file ID/path, the encoded updated catalog
record, and the ordered list of (newPath, newFileID) entries. This lets the
entire split's catalog+btree+graph effect be described by ONE record,
appended through the SAME writer/directory already used for catalog
mutations, going through the SAME `AppendAndApply` primitive. The commit
point is therefore a single fsync.

Recovery is a new dedicated pass, `split.RecoverSplitCommits`, run via
`wal.Replay`, skipping any record whose type isn't `RecordSplitCommit`
(mirrors `catalog.RecoverFromWAL`'s "skip what I don't own" convention). On
replay it re-applies catalog Put + btree Inserts + graph edges
(via `AppendEdgeIfAbsent`) together — closing the graph crash-recovery gap.

## Design decision: "release queued writers on commit" via Status timing, not guard timing
Two distinct existing mechanisms:
- `FileGuard` (per-fileID CAS guard, `TryAcquire`/`Release`/`InProgress`) —
  purely an in-process re-entrancy guard against concurrent split attempts.
- `Orchestrator.AdmitWrite` — the actual writer gate, keyed off
  `catalog.RecordStatus == StatusSplitting`.

Earlier subtasks (2b.3.2) transitioned Status: Splitting -> Split ->
Redirect across multiple steps, with an implied intermediate
`Orchestrator.EndSplit` call that would let writers back in before btree/
graph work finished. `ExecuteSplitAtomic` deliberately does NOT do this: it
requires precondition `StatusSplitting` and transitions directly to
`StatusRedirect` inside the SAME atomic apply as the btree inserts and
graph edges. Writers are only ever unblocked once the WHOLE split's effect
is durable and applied — this is what "release queued writers on commit"
means here, and it required overriding the older staged-transition
approach rather than composing it as-is.

## Ordering
`newFileIDs`/entries are derived by `sort.Strings` over the new paths
before assignment, matching `ExecuteSplitBtreeInsert`'s existing
convention — resolves the "canonical newFileIDs ordering" item flagged in
pending.md.

## Failure model (crash points) and atomicity guarantee
Four recognized hook stages inside `ExecuteSplitAtomic`, each independently
tested via crash injection + subsequent `RecoverSplitCommits`:
1. `before_commit_append` — crash before the WAL record is even appended.
   No visible effect: catalog Status is still `StatusSplitting`, old path
   still resolves to the original file ID, zero graph edges. Recovery is a
   true no-op. The split must be retried from scratch (or abandoned — see
   residual risk below).
2. `after_commit_before_catalog_put` — WAL record is durable (fsynced) but
   the in-memory apply hadn't started. Recovery replays catalog Put + btree
   inserts + graph edges and reaches full post-split state.
3. `after_catalog_put_before_btree` — catalog Put applied in-memory but
   process died before btree inserts. Recovery re-applies (idempotently)
   and reaches full state.
4. `after_btree_before_graph` — btree applied, graph edges not yet
   appended. Recovery re-applies graph edges (idempotently via
   `AppendEdgeIfAbsent`) and reaches full state.

Guarantee achieved: from the moment the `RecordSplitCommit` WAL record is
fsynced (stage 1 boundary), the split's full effect (catalog transition +
btree repoint + graph edges) is guaranteed to eventually be applied via
`RecoverSplitCommits`, and that replay is idempotent (verified in tests by
calling `RecoverSplitCommits` twice and asserting edge counts don't
double). Before that fsync, there is no partial effect to clean up — the
crash is equivalent to the split never having started.

## Explicitly NOT covered (residual risk, disclosed not silently accepted)
- `engine/btree`'s `SaveRoot` is a separate, manual/out-of-band mechanism;
  `RecoverFromWAL` no-ops on btree records. Root-pointer reconstruction
  after a real process restart is a pre-existing, unresolved gap this
  subtask does not touch.
- `FileGuard` state is in-memory only and does not survive a real process
  restart. A crash strictly before stage 1 (WAL record fsync) leaves an
  abandoned `StatusSplitting` catalog record with no lease/heartbeat/
  timeout mechanism to detect or reclaim it. This subtask narrows the
  window in which an abandoned split can leave partial, inconsistent state
  (there is now no such window past the fsync point) but does NOT add
  automatic recovery for a split that crashed before ever reaching that
  fsync.
