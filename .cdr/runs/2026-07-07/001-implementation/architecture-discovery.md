# Architecture discovery

Files read in full: `engine/btree/node.go` (353 lines), `engine/btree/latch.go`
(115 lines), `engine/btree/lookup.go` (379 lines), plus targeted reads of
`engine/btree/insert.go` (`crabInsert` loop ~L598-609, `errRestartFromRoot`
doc ~L438-462, `crabRetryHook` call sites) and `engine/btree/delete.go`
(`crabDelete` loop ~L459-470, `crabRetryHook` call sites).

Key findings:

- `latch.go`'s `nodeLatch` (mu + atomic version counter) is keyed by node ID
  in `NodeStore.latches` (map, lazily populated via `latchFor`). `WriteNode`
  (lookup.go) is the sole choke point bumping `nodeLatch.version` by exactly
  one after every durable write. This is the "real" in-process concurrency
  control; `node.go`'s on-disk `offVersion`/`Version` field is written once at
  encode time and never re-read for concurrency decisions.
- `Tree.Lookup` (lookup.go) retries via `lookupOnce` + `errOptimisticRetry`,
  re-fetching `t.Root()` on every attempt. `t.Root()` is defined in insert.go
  and takes `rootMu` (same mutex `Tree.Insert`/`Tree.Delete` use for
  root-bootstrap/root-split). `readNodeOptimistic` itself (the actual per-node
  read helper) genuinely never calls Lock/TryLock — only the outer
  `Tree.Lookup` wrapper's `t.Root()` call briefly touches `rootMu`.
- Three restart-from-root loops exist, each following the identical shape
  `for attempt := 0; ; attempt++ { ...; err := xOnce(...); if err == errX {
  continue }; return ... }`:
  - `crabInsert` (insert.go) / `errRestartFromRoot` / `crabInsertOnce`
  - `crabDelete` (delete.go) / `errRestartFromRoot` / `crabDeleteOnce`
  - `Tree.Lookup` (lookup.go) / `errOptimisticRetry` / `lookupOnce`
  Each already has an existing test-only hook fired at the error's origin
  (`crabRetryHook` in insert.go/delete.go; `optimisticRetryHook` in
  lookup.go), used by `TestCrabbingConcurrentPropagateNoDeadlock`
  (insert_test.go) and `testOptimisticReadForcedRetryDeterministic`
  (lookup_test.go) to force deterministic restarts in tests.
- No existing package-level metrics/counters exist in `engine/btree`; adding
  one alongside `nodeLatch`/`NodeStore` machinery in `latch.go` (which already
  imports `sync/atomic`) is the natural, minimal-footprint location.
