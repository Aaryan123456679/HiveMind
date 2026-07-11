# Plan

1. `engine/graph/edgelog.go`:
   - Add `nodeLocksMu sync.Mutex` + `nodeLocks map[uint64]*sync.Mutex` fields to `EdgeLog`.
   - Init `nodeLocks` in `OpenEdgeLog`.
   - Add unexported `nodeLock(id uint64) *sync.Mutex` (lazy-create-and-cache).
   - Add exported `LockNode(sourceFileID uint64) func()` (locks, returns unlock).
   - Change `AppendEdge` to acquire+defer-release the node lock around its whole body.
   - Document the new lock's purpose and its relationship to `l.mu` and to `Compact`'s usage, referencing issue #49/subtask 4.5.11.2 and the "Segment numbering must never be reused" doc comment already present.

2. `engine/graph/compact.go`:
   - Add unexported `var compactNodeLockedHook func(id uint64)` (nil in production) with a short doc comment following the `atomicCommitHook`/`crabRetryHook` convention.
   - In `Compact`'s node loop: acquire `log.LockNode(id)` before `ReadNodeAfter`; call the hook (if set) right after; on read error, unlock and return; on `maxSeg < 0`, unlock immediately (nothing to protect) and continue; otherwise, retain the unlock func in a `map[uint64]func()` (`heldNodeLocks`) for release after that node's truncate.
   - After `WriteCSR`/`saveCompactState` (unchanged ordering), in the final truncate loop: call `TruncateNode(id)`, then always call `heldNodeLocks[id]()` (even on truncate error) before moving to the next id.
   - Update package doc comment with a short new section documenting the lock-ordering fix (mirroring the existing "Segment-number reuse (second fix cycle)" section's style) - append rather than rewrite existing sections.

3. `engine/graph/compact_test.go`:
   - Add `TestCompactConcurrentAppendNotLost`: sets `compactNodeLockedHook`, spins a goroutine that calls `AppendEdge` for the SAME node concurrently with the first `Compact` call (signalling via a channel that the goroutine has started, then relying on the lock itself to serialize the actual append until after truncate), asserts the first `Compact`'s resulting `graph.dat` only reflects the pre-existing edge (not a racy partial view), then asserts a second `Compact` call picks up and correctly merges the concurrently-appended edge (summed weight). Restore `compactNodeLockedHook = nil` via `t.Cleanup`.

4. Self-consistency: run the new test standalone, the full `engine/graph` package suite, and the full `engine` module suite, all with `-race`, multiple counts for the new race test.

5. One local commit (no push), CDR conventional message format.

6. Write `self-consistency.json` and `handoff.json` with pointers only.
