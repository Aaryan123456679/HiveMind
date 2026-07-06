# Architecture discovery

Read in full: `engine/btree/latch.go` (115 lines), `engine/btree/lookup.go` (172
lines, pre-change), `engine/btree/node.go` (353 lines), and the relevant
sections of `engine/btree/insert.go` (`Tree`, `NewTree`, `Root`, `Insert`,
`crabInsert`/`crabInsertOnce`, `errRestartFromRoot`, `crabRetryHook`,
`crabRetryBackoff`, `findParent`) plus `.cdr/commits/task-2a.4.{1,2,3}.md`.

## Key facts

- `NodeStore.latches map[uint64]*nodeLatch`, lazily populated, each entry has
  a plain `sync.Mutex` (writer-only) and an `atomic.Uint64` version counter.
  `Version(nodeID) uint64` is a non-blocking atomic load — the only primitive
  the read path is allowed to touch.
- `WriteNode(nodeID, encoded)` is the sole choke point for node-content
  mutation: one `s.f.WriteAt(encoded, offset)` call (exactly `NodeSize` =
  4096 bytes, one syscall) followed by exactly one `version.Add(1)`. It does
  NOT take the node's latch itself (that's the caller's job, per the
  documented convention: `Lock` before, `Unlock` after, the `WriteNode`
  call(s), used correctly by `insertIntoLeafAndPropagate`/`propagate` in
  insert.go and their delete.go equivalents).
- `ReadNode(nodeID)` is one `s.f.ReadAt(buf, offset)` call (also exactly
  `NodeSize` bytes, one syscall) followed by pure in-memory decode
  (`DecodeLeafNode`/`DecodeInternalNode`).
- `Tree` (insert.go) wraps `*NodeStore` + `*NodeAllocator` + a `rootMu`-guarded
  `root uint64`. `Tree.Root()` is the safe way to read the current root under
  concurrent `Insert`/`Delete`.
- Blink-tree move-right recovery is already established and battle-tested
  (four rounds of fixes in 2a.4.2, one in 2a.4.3): at internal-node level,
  peek `internal.NextSibling`'s node and compare `path` against its `LowKey`
  (not its own currently-populated max key — a sparse node under-corrects);
  at leaf level, peek `leaf.NextLeaf`'s node and compare `path` against its
  first key. `crabInsertOnce` (insert.go ~L625-725) is the canonical
  reference implementation of this loop structure; the optimistic reader
  mirrors its *shape* exactly, minus all locking.
- `errRestartFromRoot` / `crabRetryHook` / `crabRetryBackoff` (insert.go
  ~L438-484) are the established "abandon this attempt, restart the whole
  operation from the root, with jittered backoff, and let tests observe the
  restart via a hook" pattern used by both `crabInsert` and `findParent`.
  The optimistic read path reuses `crabRetryBackoff` for its own retry delay
  (no reason to duplicate it) but needs its OWN sentinel error and hook,
  since its retry trigger (version mismatch) is conceptually distinct from
  a `TryLock` miss.
