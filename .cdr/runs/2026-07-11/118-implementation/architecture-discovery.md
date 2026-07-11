# Architecture discovery — subtask 4.5.4.1 (issue #41)

Fresh discovery this run (no prior context on issue #41). Read, in order:
`docs/HLD.md` (grep for wal/btree sections), `docs/LLD/wal.md` (exists, not a
scaffold stub), `engine/btree/persist.go`, `engine/btree/lookup.go`,
`engine/btree/insert.go`, `engine/wal/recovery.go`, `engine/catalog/recovery.go`,
`.cdr/memory/pending.md`'s "btree SaveRoot / WAL-replay gap" item, and
`.cdr/index/regression.jsonl` entries for subtasks 1.3.1-1.3.5 tagged
`engine/wal`/`engine/btree`.

## Key facts established

- `engine/btree/persist.go`'s `SaveRoot(store *NodeStore, rootNodeID uint64)
  error` durably persists the tree's current root node ID to a `.root`
  sidecar file (WriteAt + Sync). Its own doc comment states it is
  "deliberately NOT called from inside Insert or Delete" to avoid forcing an
  fsync on every hot-path mutation, leaving callers to call it manually "at a
  natural checkpoint boundary, or on clean shutdown".
- `LoadRoot` recovers that root ID; if the sidecar never existed, it returns
  `reservedNodeID` (0) with no error (empty-tree convention).
- `engine/btree/lookup.go`'s `NodeStore.WriteNode` (the sole choke point for
  all node content writes, both leaf and internal) does a plain `WriteAt`
  with **no** `Sync()` call of its own — node content durability is a
  pre-existing, separate, out-of-scope concern (not touched by this subtask).
- `engine/catalog/recovery.go`'s `RecoverFromWAL` replays `RecordCatalogPut`/
  `RecordCatalogDelete` via `wal.Replay`, and its `default:` case explicitly,
  intentionally no-ops on any other record type (including
  `RecordBTreeInsert`/`RecordBTreeDelete`) — its own doc comment says this is
  because "this function's job is reconstructing Catalog state specifically,
  not asserting exclusive ownership of the WAL directory's contents." This
  is NOT a bug to "fix" by making it error; it is the acceptance criteria's
  "no longer silently no-ops" being satisfied by making the *other* gap
  (SaveRoot's manual-only invocation) never let a stale/missing root survive
  a crash in the first place.
- Repo-wide grep: `RecordBTreeInsert`/`RecordBTreeDelete` referenced only in
  `engine/wal/record.go` (definitions), `engine/wal/recovery.go` (the
  `isValidRecordType` allow-list), `engine/catalog/recovery.go` (the no-op
  default case), and `engine/wal/record_test.go`/`recovery_test.go`. No
  production code path ever encodes/appends these two record types to the
  WAL. The real production path for durable btree mutations is
  `engine/split/execute.go`'s `ExecuteSplitAtomic`, which uses
  `wal.RecordSplitCommit` + `RecoverSplitCommits` instead (a completely
  separate, already-correct recovery mechanism, out of this subtask's scope).
- `engine/btree/insert.go`'s `Tree.Insert` (the concurrency-safe production
  API used by `engine/split/execute.go`, e.g. lines ~490/499/1023/1027/1172/
  1178) only changes `t.root` in exactly two places:
  1. Empty-tree bootstrap, inside `Tree.Insert` itself (`t.root = leafID`).
  2. Root split, inside `propagate` (`t.root = newRootID`), reached when a
     split at the top of the tree promotes a new root.
  Ordinary inserts into an already-installed root (the common case) never
  touch `t.root` at all — they mutate existing node content only, which
  `NodeStore.WriteNode`'s in-place `WriteAt` already durably places at the
  correct on-disk offset (readable by a fresh `NodeStore` over the same
  file) regardless of whether `SaveRoot` was ever called.
- Consequence: the *only* way a crash can "silently drop an insert from the
  recovered tree" is if `t.root` changed (bootstrap or root split) since the
  last manual `SaveRoot` call — the sidecar file then points at a stale (or,
  for bootstrap, nonexistent) root, and the recovered tree structurally
  cannot reach the new subtree even though its node content is present on
  disk.

## Chosen fix, and why it doesn't reopen the original hot-path-fsync tradeoff

`persist.go`'s doc comment specifically rejected fsync-per-mutation. Calling
`SaveRoot` automatically *only* at the two `t.root`-changing sites (bootstrap,
root split) — rather than after every single `Insert` — preserves that
tradeoff: these are rare events (once per tree's lifetime for bootstrap, and
only occasionally for root splits, since most splits are leaf/internal splits
that don't reach the root), not a per-mutation cost. This is the smallest
change that actually closes the crash window the acceptance criteria
describes, without touching the (separate, non-goal) `NodeStore.WriteNode`
durability model or `catalog.RecoverFromWAL`'s intentional no-op default.

## Impacted files actually touched

- `engine/btree/insert.go` — `Tree.Insert` (bootstrap) and `propagate`
  (root split) each now call `SaveRoot` before returning when they change
  `t.root`, propagating any `SaveRoot` error as a wrapped `fmt.Errorf`.
- `engine/btree/btree_test.go` — new
  `TestCrashBetweenInsertAndSaveRootRecovers` per the issue's exact test
  spec name.
- `engine/wal/recovery.go` and `engine/btree/persist.go` themselves are
  NOT modified — the fix lives entirely in `insert.go`'s call sites into
  `persist.go`'s existing `SaveRoot`, consistent with the issue's listed
  "impacted modules" (`engine/btree/persist.go`, `engine/btree/btree_test.go`,
  `engine/wal/recovery.go`) describing the *area* of the gap, not literal
  files requiring edits.
