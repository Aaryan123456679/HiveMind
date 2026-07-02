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

## Cross-references

- [HLD.md](../HLD.md)
- [catalog.md](catalog.md) — record store the tree points into
- [split.md](split.md) — path insertion/redirection during auto-split
- [ingestion-agent.md](ingestion-agent.md) — candidate topic shortlisting consumer
