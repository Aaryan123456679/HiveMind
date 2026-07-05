# Architecture discovery

Read (in order): `.cdr/index/task.jsonl` (task-2a.3 history, no btree entries yet),
`docs/HLD.md` (btree row: "On-disk B+Tree mapping topic paths to fileIDs,
latch-crabbing + optimistic reads"), `docs/LLD/btree.md`'s Concurrency section, then
the entire `engine/btree/` package (node.go, lookup.go, insert.go, delete.go,
scan.go, persist.go, and all *_test.go files).

## docs/LLD/btree.md Concurrency section (verbatim intent, already documented from
Phase 1 planning, never previously implemented)

- Writes: latch-crabbing -- lock parent node, lock child node, release parent.
  Standard B-Tree crabbing to allow concurrent writers in disjoint subtrees.
- Reads: optimistic -- lock-free read of node, check version counter unchanged,
  retry if it changed during the read. No reader ever blocks a writer or another
  reader.

This confirms the exact protocol the prompt anticipated: sync.Mutex-only latches for
writers, atomic version counter for optimistic readers.

## Current engine/btree/ state (Phase 1, single-threaded, confirmed by reading every
file, not assumed)

- **No locking exists at all today.** NodeStore (lookup.go) does raw ReadAt/WriteAt
  against an index file with zero synchronization. Its own doc comment explicitly
  said "NodeStore also does not do any locking: concurrency ... is explicitly
  deferred to a later, concurrency-focused subtask" -- this is that subtask.
- **Nodes are NOT in-memory objects with persistent identity.** LeafNode/InternalNode
  are plain value structs, decoded fresh from disk on every ReadNode call and
  re-encoded+written on every WriteNode call. There is no node cache, no pointer
  identity shared across goroutines/calls. This is the single most important
  discovery shaping the design: a per-node latch/version cannot live "on" a node
  object because no such persistent object exists -- it must be attached to the
  node's uint64 ID via a registry owned by NodeStore (the only long-lived, shared
  object addressable by node ID).
- **WriteNode (lookup.go) is the single choke point** all structural mutations flow
  through: writeLeaf/writeInternal (insert.go) and every mutation site in delete.go
  call WriteNode, never write the index file directly by any other path. This makes
  WriteNode the natural, minimal place to wire in the version-counter bump for this
  subtask, without touching insert.go/delete.go call sites yet.
- **On-disk node encoding already reserves an 8-byte `Version` field** (offVersion,
  node.go) with a doc comment stating "This subtask only reserves and (de)serializes
  the field; the actual CAS/atomic version-bump logic used by concurrent
  readers/writers is implemented by a later, concurrency-focused subtask" -- i.e.
  node.go's on-disk `Version` field was pre-provisioned for this subtask but the
  live in-memory atomic counter and mutex do not exist yet. Note: the on-disk
  Version field and the new in-memory atomic version counter are DELIBERATELY KEPT
  SEPARATE (see plan.md) -- the on-disk field round-trips through Encode/Decode
  as a plain uint64 data field and is not touched by this subtask; the new
  concurrency-control version counter lives in NodeStore's latch registry, keyed by
  node ID, and is what actually gets bumped by WriteNode.
- **persist.go's SaveRoot/LoadRoot** are an out-of-band, manually-invoked checkpoint
  of the root node ID only, unrelated to per-node latching. Confirmed not to
  interact with this subtask's scope.
- No latch-crabbing or optimistic-read design doc beyond docs/LLD/btree.md's short
  Concurrency section quoted above; no additional design intent found in
  docs/HLD.md beyond the one-line btree row.

## Existing test coverage (engine/btree/*_test.go), all passing pre-change

btree_test.go, delete_test.go, insert_test.go, lookup_test.go, node_test.go,
scan_test.go -- full single-threaded coverage of encode/decode, insert (incl.
splits), delete (incl. merges), lookup, prefix scan. None exercise concurrency
(expected -- this is the first concurrency-focused subtask for this package).
