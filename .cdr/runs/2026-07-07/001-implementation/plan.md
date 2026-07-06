# Plan

1. `node.go`: rewrite doc comments on `offVersion` const, `LeafNode.Version`,
   `InternalNode.Version` to describe the on-disk field accurately and point
   to `latch.go`'s `nodeLatch.version`/`NodeStore.Version`/`WriteNode`.
2. `lookup.go`: rewrite `Tree.Lookup`'s doc comment to scope the lock-free
   claim to per-node latches and call out the `t.Root()` → `rootMu` exception
   explicitly. No logic change. (Left `readNodeOptimistic`'s doc comment
   untouched — it is accurate as-is: that function truly never calls
   Lock/TryLock.)
3. `latch.go`: add `var restartFromRootCount atomic.Uint64` + exported
   `func RestartFromRootCount() uint64`.
4. Wire `restartFromRootCount.Add(1)` into the three restart-continue sites:
   `crabInsert` (insert.go), `crabDelete` (delete.go), `Tree.Lookup`
   (lookup.go) — one line each, at the existing `if err == errX { continue }`
   branch.
5. Add `restart_count_test.go`: reuse `TestCrabbingConcurrentPropagateNoDeadlock`'s
   forced-contention setup (hold a child latch, force a TryLock miss via
   `crabRetryHook`) to deterministically trigger one restart, and assert
   `RestartFromRootCount()` increased.
6. Self-consistency: `go build ./... && go vet ./... && gofmt -l .` clean;
   `go test ./btree/... -race -count=1 -timeout 10m` green.
7. One commit (docs(btree): ... type), no push.
8. Remove items 3, 4 (node.go stale doc), 5 (lookup.go overclaim) — as listed
   in pending.md — leaving SaveRoot/WAL-replay gap and node-latch registry
   eviction items untouched.
9. Write handoff.json with pointers only.
