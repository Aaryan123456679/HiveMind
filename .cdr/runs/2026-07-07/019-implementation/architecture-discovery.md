# Architecture Discovery — 2b.3.3

## Files read

- `engine/split/execute.go` (2b.3.1 `ExecuteSplitAllocateAndWrite`, 2b.3.2
  `ExecuteSplitRedirectStub`, and their doc comments — both explicitly say
  B+Tree work is deferred to 2b.3.3, and both confirm the original fileID is
  REUSED for the redirect stub; no new fileID is allocated for the old path).
- `engine/split/execute_test.go`, `engine/split/proposer_mock.go`
  (FixtureSplitPlan/FixtureFileContent fixtures, `newTestContentStoreDepsWithWAL`
  test-fixture helper conventions), `engine/split/orchestrate_test.go`
  (`newTestWAL` helper).
- `engine/btree/insert.go`: `Insert(store, alloc, rootNodeID, path, fileID)`
  (free function) and `Tree.Insert(path, fileID) error` (concurrent
  crab-latching wrapper around the same logic via `NewTree`/`Tree.root`).
  `Tree.Lookup(path) (fileID uint64, found bool, err error)` in
  `engine/btree/lookup.go`. `btree.NewTree(store, alloc, rootNodeID)`,
  `btree.OpenIndexFile`, `btree.NewNodeStore`, `btree.NewNodeAllocator`.
- `engine/catalog/record.go`: `CatalogRecord` has `PathHash uint64` (a hash
  of the topic path for verification) but does NOT store the path string
  itself. Grepped the whole repo (`grep -rln "btree\." . | grep -v /btree/`)
  and found **no existing call site anywhere** (catalog, split, graph, or
  any non-test code) that inserts a path into a `btree.Tree`. This confirms:
  the B+Tree IS the authoritative path->fileID index for this codebase (not
  a supplementary structure), and there is no pre-established
  path-encoding/wiring convention to mirror from catalog's normal
  create/append flow — that wiring simply doesn't exist yet anywhere in the
  repo. The established "convention" to follow is therefore exactly
  `btree.Tree`'s own public API: raw topic-path strings are used directly
  as keys, unencoded (see `btree/insert_test.go`'s
  `Insert(store, alloc, reservedNodeID, "auth/login", 101)` and
  `Tree.Insert("auth/login", 101)` call sites — plain path strings, no
  hashing/encoding layer).

## Key finding: `btree.Insert`/`Tree.Insert` have UPSERT semantics

`engine/btree/insert.go`'s doc comment on the free `Insert` function states
explicitly (lines ~140-143):

> If path is already present, its fileID is updated in place (upsert
> semantics) and no structural change (and therefore no split) is possible;
> rootNodeID is returned unchanged in that case.

And the implementation (`Insert`, `crabInsertOnce`) confirms this: it does
`sort.SearchStrings` for the key, and if `leaf.Keys[i] == path`, it just does
`leaf.FileIDs[i] = fileID` and returns — no leaf-split, no root change. This
holds identically for `Tree.Insert`, which is a concurrency-safe wrapper
around the exact same leaf-level upsert logic (`insertIntoLeafAndPropagate`
performs the identical `sort.SearchStrings` + exact-match-check).

## Design decision: old path's B+Tree entry

Since 2b.3.2 REUSES the original fileID for the redirect stub (the fileID
never changes, only its CONTENT and its catalog `Status`/`RedirectTargetIDs`
change), the old path's key->fileID mapping in the B+Tree is, in the strict
sense, ALREADY CORRECT after a split with zero B+Tree mutation: the old
path still maps to `originalFileID`, and `originalFileID`'s content is now
the redirect stub (2b.3.2's job) and its catalog record now carries
`RedirectTargetIDs` (also 2b.3.2's job). No B+Tree structural change is
mathematically required.

However, this subtask's implementation (`ExecuteSplitBtreeInsert`) still
issues an explicit `tree.Insert(oldPath, originalFileID)` call as the
"repoint" step, for two concrete, defensible reasons — not just as
belt-and-suspenders busywork:

1. **Correctness under upsert semantics is free and matches the subtask's
   literal acceptance criteria** ("old path still resolves, but now to the
   redirect-stub fileID"). Per the upsert doc comment above, re-inserting
   the same (path, fileID) pair is a guaranteed-safe no-op at the leaf
   level (no split, no root change, single field write) — there is no
   correctness or performance downside to making the repoint explicit
   rather than implicit-by-omission.
2. **Self-contained function, no implicit cross-subtask invariant.** If
   this function relied on "the old path must already be in the tree from
   some earlier Create call", it would be silently dependent on wiring
   that (per the repo-wide grep above) does not exist yet anywhere in this
   codebase. By explicitly calling `tree.Insert(oldPath, originalFileID)`,
   this function is correct and self-sufficient regardless of whether the
   old path was already indexed (idempotent insert-or-update), which
   matters because 2b.3.6 (single atomic WAL-covered transaction) and any
   future caller composing this function need one clear, unconditional
   post-condition: "after this call, oldPath resolves to originalFileID and
   every newPath resolves to its newFileID" — not a conditional one that
   depends on tree pre-state.

This is implemented as a single function, `ExecuteSplitBtreeInsert`, that:
performs both the new-path inserts and the old-path repoint, all via
`tree.Insert` (the concurrency-safe `*btree.Tree` wrapper, matching this
subtask's production-path expectation — not the low-level free `Insert`
function, which is what `engine/btree`'s own tests use directly but is not
this package's concern).

## Scope boundaries respected

- No graph-edge code touched (`engine/graph/` untouched) — deferred to
  2b.3.4/2b.3.5.
- No WAL/fsync wrapping added around the B+Tree writes here — `btree.Tree`'s
  own node writes are already individually durable (`NodeStore.WriteNode`),
  matching the same "each step individually durable, cross-step atomicity
  deferred to 2b.3.6" posture 2b.3.1/2b.3.2 already established.
- `engine/btree/` itself is NOT modified — only consumed via its existing
  public API (`btree.Tree`, `NewTree`, `Insert`, `Lookup`,
  `NewNodeStore`/`NewNodeAllocator`/`OpenIndexFile`).
