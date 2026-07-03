# Requirement — Subtask 1.2.1

Source: GitHub issue #2 ("[1] B+Tree index implementation, single-threaded (engine/btree/)"),
Epic/Milestone: Phase 1: Storage core (single-threaded).

## Subtask 1.2.1 — On-disk B+Tree node layout + serialization at `index/name.idx`

- **Acceptance criteria**: Leaf and internal node formats are defined and serialize/deserialize
  round-trip losslessly; `index/name.idx` file is created on first use.
- **Test spec**: `go test ./engine/btree/... -run TestNodeSerialization -race`: encode/decode leaf
  and internal nodes with varying key counts, assert equality.
- **Impacted modules**: `engine/btree/node.go`, `engine/btree/node_test.go`.

## Explicit scope boundary (from task instructions)

This subtask is ONLY:
1. Node struct definitions (leaf + internal variants).
2. Binary Encode()/Decode() round-trip for those structs.
3. A minimal file-open-or-create helper satisfying "index/name.idx file is created on first use".

Explicitly OUT of scope (deferred to later subtasks):
- Lookup (1.2.2)
- Insert / split (1.2.3)
- Delete (1.2.4)
- Prefix scan (1.2.5) — though leaf layout must accommodate a "next leaf" sibling pointer now,
  since changing the on-disk leaf format later would be a breaking change.
- Full FileManager-style paged store wiring (likely folded into a later subtask).
