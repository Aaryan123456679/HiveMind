# Architecture discovery — task-2a.4.2

Read in full: `latch.go`, `insert.go`, `node.go`, `lookup.go`, `persist.go`,
`insert_test.go`. `docs/HLD.md` / `docs/LLD/*.md` have no concurrency design
for B+Tree insert yet beyond what 2a.4.1 already documented in latch.go
itself — this subtask is genuinely new design scope, confirmed.

## Key existing facts
- Nodes are ephemeral value structs (`LeafNode`/`InternalNode`), decoded
  fresh on every `ReadNode`. No live shared node objects — concurrency state
  lives only in `NodeStore.latches` (latch.go), keyed by node ID.
- `descendToLeaf` (lookup.go) does an unprotected (no Lock) root-to-leaf walk
  used by both `Lookup` and the existing single-threaded `Insert`.
- Existing `Insert` (insert.go) is single-threaded, un-locked, and returns
  `newRootNodeID` for the caller to track. `propagateSplit` walks a
  precomputed `parentChain` (root..parent, recorded at descent time) bottom
  to top, splitting ancestors as needed; if the chain is exhausted, a brand
  new root is allocated unconditionally. This is NOT safe for concurrent
  callers: (a) no latching at all, (b) unconditional new-root allocation
  would race/duplicate if two concurrent operations both reach "chain
  exhausted" for what they each believe is the current root.
- `persist.go`'s `SaveRoot`/`LoadRoot` are an explicit, deliberately-rare,
  caller-triggered *durability* mechanism (fsync'd sidecar file), NOT an
  in-memory coordination mechanism, and are explicitly documented as NOT
  called from inside Insert/Delete to avoid a footgun forcing an fsync per
  op. No existing root-latch/mutex of any kind exists anywhere in the
  package prior to this subtask — this is new ground, confirmed by
  persist.go's own doc comments referencing "this run's
  architecture-discovery.md" (i.e. anticipating this exact subtask).
- `node.go`'s `offVersion` doc comment still says "future CAS/atomic
  version-bump logic" — stale, should point at latch.go instead (flagged as
  nice-to-have by 2a.4.1's verification, not required for this subtask;
  left as-is here to keep this change minimal and focused).

## Design conclusion
Root-pointer protection needs to be **tree-level** application state (which
node ID is "the root" right now), separate from any individual node's
`nodeLatch` (which only protects that node's *content*). Introduce a new
`Tree` type (in insert.go) wrapping `*NodeStore` + `*NodeAllocator` + a
dedicated `rootMu sync.Mutex` + `root uint64` field. This is exactly the
"tree-level mutex for the root pointer swap" the task brief anticipated.

The existing free-function `Insert` is left completely unmodified (all
existing single-threaded tests keep passing unchanged, no regressions) —
`Tree` is purely additive, the concurrency-safe entry point for 2a.4.2+.
