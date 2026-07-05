# Plan

1. Add `engine/btree/latch.go`:
   - `nodeLatch struct { mu sync.Mutex; version atomic.Uint64 }` with a doc comment
     fully specifying the locking/versioning protocol (binding for 2a.4.2-2a.4.5).
   - `NodeStore.latchFor(nodeID) *nodeLatch` -- lazy get-or-create, guarded by
     `NodeStore.latchesMu`.
   - `NodeStore.Lock(nodeID)` / `NodeStore.Unlock(nodeID)` -- public API for future
     crabbing writers.
   - `NodeStore.Version(nodeID) uint64` -- atomic load, for future optimistic readers.
2. Edit `engine/btree/lookup.go`:
   - Add `latchesMu sync.Mutex` + `latches map[uint64]*nodeLatch` fields to
     `NodeStore` struct (NewNodeStore's `&NodeStore{f: f}` literal keeps working
     since latches is lazily initialized).
   - `WriteNode`: after a successful `WriteAt`, call
     `s.latchFor(nodeID).version.Add(1)`. Document why it does not also acquire the
     latch itself (reentrancy hazard for future crabbing callers holding it already).
3. Add `TestNodeLatchFields` to `engine/btree/node_test.go`:
   - version starts at 0 for a never-written node ID.
   - one mutation (Lock -> WriteNode -> Unlock) bumps version by exactly 1.
   - 5 sequential mutations bump monotonically by 1 each, final == 5.
   - mutating node 1 does not affect node 2's version (registry isolation).
   - 50 goroutines x 20 mutations each (1000 total), each properly taking
     Lock/Unlock around WriteNode, final version == 1000 exactly, run under `-race`.
4. Do NOT touch insert.go/delete.go call sites (out of scope; documented as
   2a.4.2/2a.4.3's job).
5. Do NOT touch node.go's on-disk Version field/Encode/Decode (separate concern,
   already pre-provisioned by an earlier subtask, unrelated to this in-memory
   concurrency-control counter).
