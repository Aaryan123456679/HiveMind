---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `engine/btree/`

Status: scaffold only (`engine/btree/doc.go` placeholder). See [HLD.md](../HLD.md) for system
context.

## Purpose

A custom on-disk B+Tree, persisted at `index/name.idx`, mapping topic path strings (e.g.
`auth/oauth`) to `fileID`s from [the catalog](catalog.md).

## Operations

- Point lookup (path -> fileID)
- Prefix scan (list a topic subtree)
- Insert
- Delete

## Concurrency

- **Writes**: latch-crabbing — lock the parent node, lock the child node, then release the
  parent. Standard B-Tree crabbing to allow concurrent writers in disjoint subtrees.
- **Reads**: optimistic, lock-free — read the node, check that its version counter is unchanged,
  and retry if it changed during the read. No reader ever blocks a writer or another reader.

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
    `candidatePool` function now issues one `btree.PrefixScan` per query term and merges the
    results; `PrefixScan`'s exported signature and internal semantics remain completely
    unchanged — confirmed no edit to `engine/btree/scan.go` was needed. The per-term split now
    uses the same non-alphanumeric-run convention `rankCandidates` already uses for scoring
    (not naive whitespace splitting as this decision's text originally described), and the
    merge is bounded by two conservative caps (`perTermPoolCap`, `mergedPoolCap`) to avoid an
    unbounded multi-term fan-out cost against `PrefixScan`'s uncapped per-call result size. See
    [query-agent.md](query-agent.md#known-risks) for the full implementation writeup and
    rationale.

## Cross-references

- [HLD.md](../HLD.md)
- [catalog.md](catalog.md) — record store the tree points into
- [split.md](split.md) — path insertion/redirection during auto-split
- [ingestion-agent.md](ingestion-agent.md) — candidate topic shortlisting consumer
