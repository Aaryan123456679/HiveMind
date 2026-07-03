# Plan — subtask 1.2.5

1. Add `engine/btree/scan.go`:
   - `ScanEntry{Path string; FileID uint64}` result type.
   - `PrefixScan(store *NodeStore, rootNodeID uint64, prefix string) ([]ScanEntry, error)`:
     - Descend via the existing shared `descendToLeaf(store, rootNodeID, prefix)` helper
       (same helper `Lookup`/`Insert` already use) to land on the first leaf that could
       contain prefix-matching keys.
     - `sort.SearchStrings(leaf.Keys, prefix)` to find the starting index within that leaf.
     - Loop: for each key from the starting index forward, if it has `prefix` (via
       `strings.HasPrefix`) append a `ScanEntry`; the moment a key does not have the prefix,
       return immediately (early exit, valid due to sorted-key invariant).
     - On exhausting a leaf's keys without an early exit, follow `NextLeaf` (via
       `store.ReadNode`) to the next leaf and continue from index 0; stop when `NextLeaf ==
       noSibling`.
     - Do not special-case `rootNodeID == reservedNodeID`; let it surface `ReadNode`'s
       existing "reserved and never valid" error, symmetric with `Lookup`.
2. Add `engine/btree/scan_test.go`:
   - Reuse `newTestStoreAndAllocator` (defined in `insert_test.go`) for real
     NodeStore/NodeAllocator/Insert-backed tree construction (not the older fake-tree
     scaffolding in `lookup_test.go`).
   - `TestPrefixScan` (exact required name): insert a mixed set of topic paths across
     several distinct top-level prefixes, assert `PrefixScan` returns exactly the expected
     subset, in sorted order, for several prefixes including one matching zero keys and the
     empty-string prefix (matches everything).
   - `TestPrefixScanNoMatches`: a prefix matching zero keys, as its own dedicated test.
   - `TestPrefixScanAcrossLeafBoundary`: insert enough keys sharing one prefix to force
     multiple leaf splits, confirming `NextLeaf`-following collects all of them without
     spilling into a following, differently-prefixed leaf.
   - `TestPrefixScanPrefixIsCompleteKey`: a prefix that is itself a complete, previously
     inserted key, plus other keys that extend it, confirming both the exact match and its
     extensions are included.
3. Run `go build ./engine/...`, `go vet ./engine/...`, `gofmt -l` on new files, and
   `go test ./engine/btree/... -race -v -count=1` to confirm no regressions across
   1.2.1-1.2.4's existing tests plus the new tests all pass.
4. Update `.cdr/index/file.jsonl` (two new entries for `scan.go`/`scan_test.go`) and
   `.cdr/index/task.jsonl` (`task-1.2.5` -> `implemented`, with this run's ID and the
   post-commit SHA).
5. One local commit (Problem/Solution/Impact style, no push).
6. Write `validation-matrix.json`, `self-consistency.json`, `handoff.json`.

## Explicitly out of scope for this run

- Root-node-ID sidecar persistence, `LoadRoot`/`SaveRoot`, and close/reopen round-trip
  testing (belongs to subtask 1.2.6 per the issue's actual checklist -- see requirement.md's
  scope-correction finding).
- Any change to `engine/btree/delete.go`'s tombstone/reclamation gap.
