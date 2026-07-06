# Requirement — btree pending.md cleanup (3 of 5 items)

Source: `.cdr/memory/pending.md`, post Epic 2a closure (issue #9 fully closed).

1. **node.go stale doc comments**: `LeafNode.Version`/`InternalNode.Version` and
   `offVersion` doc comments still describe the on-disk version field's
   CAS/atomic bump logic as future work. Task-2a.4.1 resolved this by building
   a separate in-memory-only counter in `latch.go` (`nodeLatch.version`)
   instead. Update doc comments to point at `latch.go`'s `NodeStore.Version`/
   `WriteNode` and state plainly the on-disk field is not used for in-process
   concurrency control.

2. **lookup.go overclaim**: `Tree.Lookup`'s doc comment says the read path
   "never calls Lock/TryLock anywhere," but its retry loop calls `t.Root()`,
   which briefly takes `rootMu` (shared with `Tree.Insert`/`Tree.Delete`).
   Reword to scope the lock-free guarantee to per-node latches, noting the
   `rootMu` acquisition via `t.Root()` as a narrow, intentional, documented
   exception. Doc-only; no logic change.

3. **No retry-attempt observability**: `insert.go`/`delete.go`/`lookup.go`'s
   restart-from-root loops (`crabInsert`, `crabDelete`, `Tree.Lookup`) have no
   cap (intentional) but also no visibility into retry-storm pathology. Add a
   purely additive `atomic.Uint64` counter + exported `RestartFromRootCount()`
   accessor, incremented once per restart in each of the three loops. No
   behavioral change to retry/backoff logic. Add a test forcing a real
   restart and asserting the counter moves.

Constraints: do not touch `engine/split/` (concurrent agent working there on
2b.1.1). Doc-only for items 1-2; additive-only instrumentation for item 3.
`go build`/`go vet`/`gofmt` clean and `go test ./btree/... -race -timeout 10m`
green with zero regressions. One local commit, no push.
