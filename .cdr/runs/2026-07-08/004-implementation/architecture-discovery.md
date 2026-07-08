# Architecture discovery — subtask 3.1.2

## Sources read (index-first order)
1. `.cdr/index/task.jsonl` — task-2b.3.4, task-2b.3.5, task-2b.3.6, task-3.1.1 entries.
2. `.cdr/memory/pending.md` — the flagged edge_append.go crash-recovery-replay gap.
3. `docs/LLD/graph.md` — module LLD (storage layout, edge shape, traversal API).
4. `.cdr/runs/2026-07-08/001-planner` .. `003-cdr-commit` — 3.1.1's full artifact trail.
5. `engine/graph/edge_append.go`, `engine/graph/csr.go` — targeted source read (after
   indexes exhausted), to confirm exact existing primitives/conventions before designing.

## docs/LLD/graph.md summary
- `graph.dat`: CSR-like compact adjacency arrays per source fileID, persisted via
  whole-snapshot rewrite (3.1.1, done).
- "Writes append-only per-node edge logs, avoiding need to lock shared adjacency array" —
  this is 3.1.2, not yet implemented; LLD explicitly names it as a distinct mechanism from
  the CSR persistence primitive.
- Edge shape: `{ targetFileID, edgeType, weight, lastUpdated }`.
- Edge types: ENTITY_COOCCUR (weighted), LLM_ASSERTED, SPLIT_SIBLING, REDIRECT.
- 3.1.3 (compaction) "merges edge log into array" — confirms 3.1.2's log is the *input* to
  3.1.3's compaction, i.e. a distinct on-disk artifact from graph.dat.

## Scope question: edge_append.go's `EdgeAppender` vs. a new per-node writer

**Resolved: 3.1.2 is a genuinely new, additional mechanism. It does not modify, replace, or
subsume `edge_append.go`'s `EdgeAppender`.**

Evidence:
1. **Issue #15's own "Impacted modules" for 3.1.2** names `engine/graph/edgelog.go,
   engine/graph/edgelog_test.go` — new files, not `edge_append.go`.
2. **Different purpose/shape.** `EdgeAppender` (task-2b.3.4, issue #12) is a single
   shared append-only log rooted at *one* directory, used narrowly by
   `engine/split/execute.go`'s `ExecuteSplitGraphEdges` to durably record SPLIT_SIBLING/
   REDIRECT edges as part of the atomic split-commit WAL transaction (task-2b.3.6). It only
   accepts `EdgeType ∈ {EdgeSplitSibling, EdgeRedirect}` and its `Edge` struct has no
   Weight/LastUpdated fields (split edges don't need them).
   3.1.2's per-node edge log is the *general* durable landing zone for **all** new edges
   discovered by ingestion (ENTITY_COOCCUR with weight, LLM_ASSERTED) as well as split
   edges going forward, organized **one log per source fileID** specifically so that
   concurrent writers touching different fileIDs (e.g. two ingestion workers processing
   different files at once) never contend on a single shared lock/directory. Its edge
   shape must include Weight/LastUpdated (per LLD's `{targetFileID, edgeType, weight,
   lastUpdated}`) because 3.1.3's compaction needs to merge/increment ENTITY_COOCCUR
   weights from this log — `EdgeAppender`'s narrower `Edge` shape cannot represent that.
3. **The flagged pending.md crash-recovery-replay gap on `EdgeAppender` is already
   RESOLVED**, per `.cdr/memory/pending.md` line 23 and `.cdr/index/task.jsonl`
   task-2b.3.6: `ExecuteSplitAtomic`'s `wal.RecordSplitCommit` wraps `EdgeAppender`
   writes inside the same WAL transaction as catalog/btree, and `RecoverSplitCommits`
   replays them idempotently via `AppendEdgeIfAbsent`. There is no longer an open,
   not-yet-assigned gap for 3.1.2 to "close" — that follow-up already has an owner
   (2b.3.6) and is marked RESOLVED. 3.1.2 is therefore free to be scoped purely per issue
   #15's own acceptance criteria (per-node non-blocking append log), not as a superseding
   replacement for `EdgeAppender`.
4. **Reuse without merging**: 3.1.2 reuses the same underlying primitive `EdgeAppender`
   already reuses — `engine/wal.Writer`/`wal.OpenWriter`/`wal.ReadSegment` — because that
   is the repo's established low-level append-only segment-writer convention (also used by
   `EdgeAppender` itself). This is convention reuse, not code-path reuse: 3.1.2's
   `edgelog.go` gets its own `EdgeLog` type managing *N* per-fileID `wal.Writer` instances
   (one per source fileID subdirectory) rather than the single shared writer
   `EdgeAppender` wraps. `edge_append.go` is left completely untouched by this subtask
   (matches 3.1.1's own precedent of "Zero changes to existing edge_append.go log").

## Non-blocking design rationale
`wal.Writer` (`engine/wal/writer.go`) already has its own internal `sync.Mutex` guarding
each instance's segment-rotation/append state. Giving each source fileID its own
`wal.Writer` instance means concurrent appends to *different* fileIDs contend on
*different* mutexes (true non-blocking), while appends to the *same* fileID are correctly
serialized by that fileID's own writer's mutex (matches "per-node log" semantics — a single
node's log is still an ordered, single-writer-at-a-time append log). The `EdgeLog`
manager's own map of fileID -> `*wal.Writer` uses a `sync.RWMutex` with a double-checked-
lock pattern: the common case (writer already exists) only takes a brief `RLock`, so the
manager-level lock is not a bottleneck across distinct, already-opened per-node logs
either.

## Decision: no expansion of `EdgeType` enum in this subtask
`edge_append.go` only defines `EdgeSplitSibling`/`EdgeRedirect` (+ `EdgeTypeInvalid`
sentinel). Issue #15 explicitly assigns "Edge-type support: ENTITY_COOCCUR (weighted),
LLM_ASSERTED, SPLIT_SIBLING, REDIRECT" to subtask 3.1.4, with its own impacted module
`engine/graph/edge.go`. 3.1.2's per-node log format must be able to *store* a full
`{Target, Type, Weight, LastUpdated}` record shape (reusing `csr.go`'s existing `CSREdge`
type verbatim, since it already has exactly this shape) but must not itself introduce new
`EdgeType` constants or type-specific creation helpers (e.g. no `AppendEntityCooccur`
convenience API) — that is 3.1.4's job. `edgelog.go` only rejects the `EdgeTypeInvalid`
zero-value sentinel, mirroring the minimal validation precedent already used elsewhere
(`csr.go`'s `decodeCSREdge` intentionally does not validate the on-disk type byte either,
per the 3.1.1 non-blocking finding already tracked in pending.md).
