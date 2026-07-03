# Requirement — Subtask 1.2.2 (GitHub issue #2)

Source: `gh issue view 2` — Epic "Phase 1: Storage core (single-threaded)", module `engine/btree/`.

## Subtask 1.2.2 — Point lookup (path -> fileID)

- **Acceptance criteria**: Lookup returns the correct fileID for an inserted path;
  returns a well-defined not-found result for an absent path.
- **Test spec**: `go test ./engine/btree/... -run TestLookup`: insert a fixed set of
  paths, assert lookup correctness for both present and absent keys.
- **Impacted modules**: `engine/btree/lookup.go`, `engine/btree/lookup_test.go`.

## Context from the epic

- 1.2.1 (node layout + serialization) is `verified` — `LeafNode`/`InternalNode`,
  `Encode`/`Decode*`, `NodeSize = 4096`, `OpenIndexFile` already exist and are frozen.
- 1.2.3 (insert with node-splitting) has NOT landed yet. This subtask must not
  implement real insert; it needs only a minimal, explicitly-scoped-as-temporary
  test-only tree-building helper to get data on disk for Lookup to traverse.
- 1.2.4 (delete) and 1.2.5 (prefix scan) are out of scope.

## Out of scope for 1.2.2

- Node-splitting insert logic (1.2.3).
- Delete / merge-or-tombstone (1.2.4).
- Prefix scan (1.2.5).
- Latch-crabbing / optimistic-read concurrency protocol (later, concurrency-focused
  subtask per docs/LLD/btree.md's "Concurrency" section — Version field already
  reserved by 1.2.1 but not used yet).
- A full page-allocator/free-list analogous to `engine/catalog/file.go`'s
  `FileManager` (deferred; nodes are addressed by a minimal sequential node-ID scheme
  established in this subtask, see architecture-discovery.md).
