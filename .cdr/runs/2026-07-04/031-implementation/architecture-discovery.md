# Architecture discovery — subtask 1.2.5

## Reading order followed

1. `.cdr/memory/*` — all empty (decisions.md, pending.md, regression-routes.md, state.md,
   timeline.md have no content specific to this subtask yet).
2. `docs/HLD.md` — not re-read in full this run beyond what's cross-referenced from
   `docs/LLD/btree.md` (system-level context already established by prior 1.2.x runs).
3. `docs/LLD/btree.md` — confirmed the prior agent's note: prefix scan is only mentioned at
   the "Operations" list level ("Prefix scan (list a topic subtree)") with **no** detailed
   scan semantics (no mention of NextLeaf, no early-exit rule, no persistence semantics). The
   "Concurrency" section describes latch-crabbing/optimistic-read policy that is explicitly
   out of scope for this (still single-threaded) subtask. Confirmed independently: this LLD
   gives no additional constraints beyond the issue's own acceptance criteria/test spec.
4. `.cdr/index/file.jsonl` — confirms `engine/btree/lookup.go`'s `NextLeaf`/leaf-chaining was
   explicitly built in 1.2.1/1.2.2 anticipating "a future prefix/range scan (subtask 1.2.5)"
   (see `node.go`'s `LeafNode.NextLeaf` doc comment) — validates the NextLeaf-based scan
   design as the intended approach, not a new invention.
5. `.cdr/index/task.jsonl` — confirms task-1.2.1 through task-1.2.4 all `state: "verified"`;
   no task-1.2.5/1.2.6 entries yet.
6. `.cdr/index/regression.jsonl` — confirms the known root-node-ID persistence gap
   (documented across 1.2.3's `NodeAllocator` doc comment and 1.2.4/030-verification's
   regression note), and confirms it is explicitly associated with "1.2.5/1.2.6" as a pair,
   not attributed solely to 1.2.5 -- consistent with the scope-correction finding in
   requirement.md.
7. `engine/btree/node.go` (full) — `LeafNode.NextLeaf`, node header layout, `noSibling`
   sentinel (0), `reservedNodeID` convention (0 reserved, real IDs start at 1).
8. `engine/btree/lookup.go` (full) — `NodeStore` (ReadNode/WriteNode by nodeID *
   NodeSize offset), `descendToLeaf` (shared descent helper used by Lookup and Insert; not
   previously used by anything scan-related), `Lookup`'s not-found=nil-error convention
   (adopted for PrefixScan's no-matches case too).
9. `engine/btree/insert.go` (full) — `NodeAllocator` (sidecar `.nodealloc` file,
   WriteAt+Sync idiom), `Insert`'s root-ID return-value convention (caller must track
   `newRootNodeID` across calls; no in-memory or on-disk "current root" state lives inside
   NodeStore/NodeAllocator itself). Confirms there is genuinely no persisted root pointer
   today, and that adding one is a distinct, not-yet-assigned decision (matches the
   scope-correction finding).
10. `engine/btree/delete.go` (skimmed relevant sections, not the entire tombstone-leaf
    rebalancing logic in depth, since scan does not touch delete's internals) — confirmed
    delete does not alter `LeafNode.NextLeaf` wiring incorrectly (tombstoned/emptied leaves
    still keep correct NextLeaf pointers per 1.2.4's fix), so a scan running after deletes
    will not skip or double-count leaves. The known 1.2.4 gap (unreclaimed tombstoned leaf
    IDs / abandoned merged-away node IDs) does not affect scan correctness: PrefixScan never
    visits a node except by following root->leaf descent or NextLeaf pointers, both of which
    only ever point at live, reachable nodes.
11. `engine/btree/insert_test.go` (partial, `newTestStoreAndAllocator` helper) — confirmed
    the established test-scaffolding convention (real `NodeStore`/`NodeAllocator`/`Insert`
    over `t.TempDir()`, not the older `lookup_test.go` fake-tree scaffolding) that this run's
    new `scan_test.go` should reuse for consistency with 1.2.3/1.2.4.

## Design decision

`PrefixScan(store *NodeStore, rootNodeID uint64, prefix string) ([]ScanEntry, error)`:
descends via the existing shared `descendToLeaf` helper (as if looking up `prefix` as a key)
to find the first leaf that could contain prefix-matching keys, then scans forward within
that leaf and across `NextLeaf`-linked sibling leaves, collecting entries whose key has
`prefix` as a string prefix, with an early exit as soon as a key no longer has the prefix
(valid because keys are kept in tree-wide sorted order, which is an insert/delete-maintained
invariant, not a new one this subtask introduces). Chosen over an iterator/callback style
for symmetry with `Lookup`'s existing plain-return-value shape, and because the issue's own
test spec ("assert prefix scan returns exact expected subset") implies comparing a
materialized result set, not incrementally consuming a callback stream.

`rootNodeID == reservedNodeID` (never-inserted-into tree) is deliberately NOT special-cased,
mirroring `Lookup`'s identical, already-documented "out of scope" stance for that case
(both surface whatever error `ReadNode` naturally returns for node ID 0). This preserves
symmetry between the two read-path operations rather than introducing a new, scan-only
convention.
