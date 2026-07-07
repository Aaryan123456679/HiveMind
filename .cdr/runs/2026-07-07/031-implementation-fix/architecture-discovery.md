# Architecture discovery (fix cycle)

- `engine/catalog/content.go`: `ContentStore.stripes [numStripes]sync.Mutex`
  (unexported), `stripeFor(fileID) = fileID % numStripes` (unexported,
  package-level func in `catalog.go`). `Append` (line ~336) and `ReadPartial`
  (line ~491) both lock `cs.stripes[stripeFor(fileID)]` for their entire
  critical section via `defer`. `InvalidateHeaderCache` (line ~395) takes only
  the separate `cs.headerCacheMu`, documented as safe to call while already
  holding a `cs.stripes` entry.
- `engine/split/execute.go`: `ExecuteSplitRedirectStub` (~line 277) and
  `ExecuteSplitAtomic` (~line 735) are the two split-commit paths that mutate
  `originalFileID`'s content and catalog record. Neither took any
  `cs.stripes` lock before this fix. `ExecuteSplitAtomic` wraps its commit
  sequence in a single `wal.AppendAndApply` closure containing (in order):
  `cat.Put`, header-cache invalidation, B+Tree inserts (new paths + old
  path repoint), graph-edge appends -- with existing named
  `atomicCommitHook` test checkpoints between each stage
  (`before_commit_append`, `after_commit_before_catalog_put`,
  `after_catalog_put_before_btree`, `after_btree_before_graph`).
- `engine/split/guard.go`'s `FileGuard`: independent in-memory CAS map keyed
  by fileID, acquired by the caller (e.g. an orchestrator) before calling
  either split-commit function, to serialize concurrent *splits* of the same
  fileID. Entirely separate lock domain from `cs.stripes`; no shared state,
  so no ordering interaction possible.
- `engine/catalog/catalog.go`: `Catalog` has its OWN, independent
  `stripes [numStripes]sync.Mutex` array (`stripeFor` is shared/reused but the
  arrays themselves are distinct instances), taken inside `cat.Put`. `Append`
  already nests `cs.stripes -> cat.stripes` (acquires `cs.stripes` first, then
  calls into `cat.Put` which takes `cat.stripes` internally) -- this fix
  preserves that exact nesting order for both split-commit paths, so no new
  lock-order inversion is introduced relative to the pattern the codebase
  already establishes and stress-tests.
- `wal.AppendAndApply`: takes the `wal.Writer`'s own internal lock/fsync path.
  `Append` already calls this while holding `cs.stripes`, establishing
  `cs.stripes -> wal.Writer-internal` as an existing, already-tested nesting
  order; this fix makes `engine/split` participate in the same order for the
  first time, not a new one.
