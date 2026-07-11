# Plan — 4.5.12.4 NodeAllocator durability check

1. In `engine/btree/insert.go`, add `maxNodeIDInStore(store *NodeStore)
   (uint64, error)`:
   - `store.f.Stat()` -> size.
   - `n := uint64(size) / uint64(NodeSize)`; if `n == 0`, return `0, nil`
     (no node slots ever written).
   - else return `n - 1, nil` (node ID `n-1` is the highest slot whose bytes
     have been written, since node ID `k` occupies
     `[k*NodeSize, (k+1)*NodeSize)` and IDs are handed out with no gaps).
   - Wrap any Stat error with context (`"btree: nodealloc: stat ... for
     cross-check: %w"`).
2. In `NewNodeAllocator`, after restoring `next` from the sidecar (existing
   code, unchanged) and before constructing/returning `&NodeAllocator{...}`:
   - call `maxPresent, err := maxNodeIDInStore(store)`; propagate error
     (closing `f` first, matching existing error-path convention in this
     function).
   - if `maxPresent > next`, close `f` and return a descriptive error
     (mirroring `NewIDAllocator`'s wording): sidecar high-water-mark is
     behind the highest node ID actually present in the index file; refusing
     to hand out node IDs that could collide with ones already in use.
3. Extend `NewNodeAllocator`'s doc comment to describe the new cross-check
   (mirroring `IDAllocator`'s doc comment style), and remove/update the
   "Known gap ... expected to be revisited by 1.2.5/1.2.6" doc comment on
   `NodeAllocator` (the struct) if it references exactly this now-closed gap
   — check wording carefully; that comment is actually about root-pointer
   persistence (a different gap, closed by persist.go/SaveRoot), not this
   ID-reuse cross-check, so it likely stays as-is. Verify before editing.
4. Append `TestNodeAllocatorCrossChecksExistingNodes` to
   `engine/btree/insert_test.go`, with its own doc-comment header noting it
   is subtask 4.5.12.4's addition (append-only discipline, so 4.5.12.6 can
   append after it later without collision). Cover:
   - Non-error case: allocate a few real node IDs via `Next()` +
     `WriteNode`, close, reopen via a fresh `NewNodeAllocator` against the
     same store -- must succeed and continue from the correct high-water
     mark.
   - Error case: manually grow/write the index file so a node ID higher than
     the `.nodealloc` sidecar's recorded high-water-mark is present (simulate
     "sidecar lost/reverted to stale value"), then call `NewNodeAllocator`
     again -- must return a non-nil error, and must NOT silently allow
     `Next()` to return a colliding ID.
5. Self-consistency: `go build ./...`, `go vet ./engine/btree/...`,
   `go test ./engine/btree/... -run TestNodeAllocatorCrossChecksExistingNodes
   -v`, then full `go test ./engine/btree/...` and `-race` for the package,
   confirming no regression to existing tests (in particular
   TestLookupInternalNodeMultiKeyRouting from 4.5.12.3 and
   TestCrashBetweenInsertAndSaveRootRecovers from issue #41).
6. `git diff --cached --stat` before commit to confirm scope is only
   `engine/btree/insert.go` + `engine/btree/insert_test.go` (+ this run's own
   `.cdr/runs/...` docs), unstage anything unrelated left by other in-flight
   agents.
7. One local commit, no push.
