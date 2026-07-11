---
last_synced_commit: 67248b1b62a7906d7735b5ea14a829e43b5d8de0
---

# LLD: `engine/btree/`

Status: scaffold only (`engine/btree/doc.go` placeholder). See [HLD.md](../HLD.md) for system
context.

## Purpose

A custom on-disk B+Tree, persisted at `index/name.idx`, mapping topic path strings (e.g.
`auth/oauth`) to `fileID`s from [the catalog](catalog.md).

## Operations

- Point lookup (path -> fileID)
- Prefix scan (list matching topic entries — see the byte-prefix-vs-subtree caveat below and in
  [Known risks](#known-risks))
- Insert
- Delete

### Persistence: `.root` / `.nodealloc` sidecars and caller-owns-commit-timing

The tree's on-disk state is spread across three files alongside the main index file
(`index/name.idx`):

- **`index/name.idx`** — the node store itself (`NodeStore`, see `node.go`/`lookup.go`): fixed-size
  node slots, node ID `N` always at byte offset `N * NodeSize`.
- **`index/name.idx.nodealloc`** — `NodeAllocator`'s (`insert.go`) durably-persisted
  high-water-mark node ID, restored on `NewNodeAllocator(store)` and advanced (`WriteAt` + `Sync`)
  on every `Next()` call before a new ID is handed out, so a reopen never reissues an
  already-used node ID.
- **`index/name.idx.root`** — the tree's current root node ID, written by `SaveRoot(store,
  rootNodeID)` and recovered by `LoadRoot(store)` (`persist.go`). A missing `.root` sidecar (e.g. a
  brand-new index) is a normal, non-error case: `LoadRoot` returns `reservedNodeID` (0).

**Caller-owns-commit-timing design**: `SaveRoot` is deliberately **not** called from inside the
package-level `Insert`/`Delete` functions — both already return the possibly-new root node ID to
their caller on every call, and wiring persistence into them would force an `fsync` on every single
insert/delete with no acceptance criterion requiring it. Callers of the raw `Insert`/`Delete`
functions own deciding *when* a new root is durably committed (e.g. once per batch, at a checkpoint
boundary, or on clean shutdown) and call `SaveRoot` explicitly at that point.

`Tree.Insert` (the higher-level, latch-crabbing-aware wrapper, `insert.go`) partially supersedes
this: it automatically calls `SaveRoot` whenever it installs a brand-new root (bootstrap or a
root-level split), closing the crash-window gap between a root-changing insert and the caller's
next manual `SaveRoot` call. It deliberately does **not** auto-checkpoint on every insert (only on
root-changing ones), preserving the original caller-owns-commit-timing rationale for the common
case where a node updates in place without a new root.

**Durability cross-check (`NodeAllocator.Next`)**: because a `.nodealloc` sidecar can be lost,
restored from a stale backup, or simply never created against a non-fresh index file, `Next()`
cross-checks its candidate ID, on every call, against `maxNodeIDInStore` — the highest node ID
actually durably present in the index file (derived from the file's on-disk size, since node IDs
are handed out strictly monotonically with no gaps or reuse). If the candidate would collide with
an already-present node, `Next` returns a descriptive error instead of silently reissuing an
already-used ID that would let a subsequent `WriteNode` corrupt existing data. This check runs
lazily (inside `Next`, not eagerly in `NewNodeAllocator`), unlike `engine/catalog/idalloc.go`'s
analogous `NewIDAllocator` check, because a `NodeAllocator` can legitimately be constructed
read-only against a store with content written outside the allocator (e.g. hand-built test
fixtures).

**Stale-`SaveRoot`-recovery guarantee**: if a caller inserts more keys via the raw, package-level
`Insert` after its last `SaveRoot` call and then closes/reopens the index file with no intervening
`SaveRoot`, `LoadRoot` recovers the **stale**, last-checkpointed root — not the newer root that
would reflect the un-checkpointed inserts. This is a documented, bounded degradation, not silent
corruption: every checkpointed key remains 100% intact and correctly `Lookup`-able through the
stale root; un-checkpointed keys either correctly report not-found or resolve to their correct
`fileID` (never a wrong one) depending on whether they landed under the stale root's own subtree;
and `PrefixScan` through the stale root still recovers every key, checkpointed and un-checkpointed
alike, since its leaf-chain traversal is not confined to the stale root's own subtree. `Tree.Insert`
does not have this exposure for root-changing inserts (see above), so this applies specifically to
direct callers of the raw `Insert`/`Delete` functions.

## Concurrency

- **Writes**: latch-crabbing — lock the parent node, lock the child node, then release the
  parent. Standard B-Tree crabbing to allow concurrent writers in disjoint subtrees. Concurrent
  split propagation across multiple levels uses `TryLock`-and-restart-from-root rather than
  blocking waits: if a writer can't acquire a needed latch without risking deadlock against another
  concurrent splitter, it releases what it holds and restarts its whole operation from the root
  (observability: a restart-attempt counter tracks how often this happens).
- **Reads**: optimistic, lock-free — read the node, check that its version counter is unchanged,
  and retry if it changed during the read. No reader ever blocks a writer or another reader. Two
  distinct version counters exist per node: an in-memory-only counter (`nodeLatch.version`, keyed
  by node ID) that optimistic reads actually check via `NodeStore.Version`, and a separate on-disk
  copy of the version counter (encoded in every node's header) that is written whenever a node is
  persisted but is not itself read back by the optimistic-read path — callers must always go
  through `NodeStore.Version(nodeID)`, never decode the on-disk field directly, for this mechanism
  to be correct.
- **Known asymmetric self-healing property**: `Lookup`'s leaf-level `NextLeaf` move-right recovery
  (following the sibling chain forward when a descent lands one leaf short) is right-only — it can
  mask a left-clamp/under-route bug in the internal-node routing step but does not mask a
  right-clamp/over-route one. This is an intentional, accepted consequence of B-link-tree-style
  leaf-chain traversal, not a defect; it means lookup is robust to some but not all classes of
  routing mistakes, which is worth keeping in mind when reasoning about routing-path test coverage
  (see `.cdr/index/regression.jsonl`'s 4.5.12.3 finding, run `114-verification`,
  originally logged only in that run's `verification.json` and retroactively
  appended to `regression.jsonl` by subtask 4.5.18.2).

## Interactions with other modules

- `catalog/` — the B+Tree resolves a topic path to a `fileID`, then the catalog record for that
  `fileID` is the source of truth for version/status/size.
- `split/` — when a file splits, new topic paths are inserted into the tree pointing at newly
  allocated `fileID`s, and the old path's entry may be redirected rather than deleted (see
  [split.md](split.md)).
- `ingestion-agent` (`agents/ingestion/`) — the shortlisting step that prevents topic-boundary
  nondeterminism reads a prefix scan / candidate set from this index before invoking the
  segmentation LLM.

## Known risks

- None unique to this module beyond the general correctness bar for latch-crabbing
  implementations (must be validated under `go test -race`, per the engine-wide convention in
  [AGENT.md](../../AGENT.md)).
- **`PrefixScan` matches by byte-prefix, not by path-segment subtree** (issue #50, subtask
  4.5.12.5). `prefix` is matched with a plain leading-bytes comparison, so a prefix of `"auth"`
  also matches an unrelated key like `"authorize/grant"`, which merely shares leading bytes and is
  not part of the `"auth"` subtree. Callers that want true subtree scoping — only keys nested under
  a given path, not merely sharing its leading bytes — must include the trailing path separator
  themselves (e.g. pass `"auth/"` rather than `"auth"`). See `PrefixScan`'s doc comment in
  `engine/btree/scan.go` for the full explanation.
- **`PrefixScan` is a literal-prefix-only query primitive — no multi-term/fuzzy query support.**
  `PrefixScan` matches only paths whose leading bytes equal a supplied prefix string; it has no
  concept of "any of these terms" or "these terms in any order". This was flagged as a
  `design_limitation` (non-blocking) during task 4.2.1 (issue #21, commit `b8ebc64`,
  `.cdr/index/regression.jsonl`), because `engine/rpc/search_candidates.go`'s term-overlap
  ranking delegates candidate-**pool selection** entirely to a single `PrefixScan` call on the
  query's first whitespace-separated token — so a multi-word natural-language query whose first
  token is not itself a path-leading segment (e.g. "how do I configure the graph database")
  returns zero candidates before term-overlap ranking ever runs.
  - **Decision (issue #47, subtask 4.5.9.1)**: `btree` itself is **not** extended with a new
    non-prefix query primitive. The chosen fix lives one layer up, in the RPC caller
    (`engine/rpc/search_candidates.go`): issue one `PrefixScan` per whitespace-separated query
    term (not just the first) and merge the resulting `ScanEntry` sets (deduplicated by
    `FileID`/`Path`) into one pool before ranking. `PrefixScan`'s signature and semantics are
    unchanged by this decision — see [query-agent.md](query-agent.md#known-risks) for the full
    rationale and the still-open residual gap this does not close. Actual implementation is
    deferred to subtask 4.5.9.2 (this subtask, 4.5.9.1, is decision + documentation only).
  - **Implemented (issue #47, subtask 4.5.9.2)**: `engine/rpc/search_candidates.go`'s new
    `candidatePool` function now issues one `btree.PrefixScan` per *distinct* query term and
    merges the results; `PrefixScan`'s exported signature and internal semantics remain
    completely unchanged — confirmed no edit to `engine/btree/scan.go` was needed. The
    per-term split now uses the same non-alphanumeric-run convention `rankCandidates` already
    uses for scoring (not naive whitespace splitting as this decision's text originally
    described).
    - **Correction (CHANGES_REQUESTED re-fix, `.cdr/runs/2026-07-11/110-verification`)**:
      this section previously claimed the merge is "bounded by two conservative caps
      (`perTermPoolCap`, `mergedPoolCap`) to avoid an unbounded multi-term fan-out cost" —
      that overstated what those two caps do. `btree.PrefixScan` (see this file's
      leaf-chain-following implementation in `scan.go`) already completes its full
      traversal and returns every matching entry before `candidatePool` ever gets to
      truncate the result to `perTermPoolCap`/`mergedPoolCap` entries, so those caps bound
      only *retained pool memory*, not scan *cost* (the number/cost of `PrefixScan` calls
      issued). What actually bounds worst-case scan cost now is `candidatePool`
      deduplicating the query's terms before the scan loop (so a query repeating one term
      N times is scanned once, not N times) plus `maxQueryTerms`, a hard cap (32) on the
      number of *distinct* terms a request may have, enforced in `SearchCandidates`
      (`server.go`) as request validation (`codes.InvalidArgument`) before any
      `PrefixScan` call is issued. See [query-agent.md](query-agent.md#known-risks) for the
      full implementation writeup, rationale, and regression coverage for this correction.

## Cross-references

- [HLD.md](../HLD.md)
- [catalog.md](catalog.md) — record store the tree points into
- [split.md](split.md) — path insertion/redirection during auto-split
- [ingestion-agent.md](ingestion-agent.md) — candidate topic shortlisting consumer
