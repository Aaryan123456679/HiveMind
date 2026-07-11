# Requirement — Issue #50, subtask 4.5.12.4

Source: GitHub issue #50 ("[4.5] engine/btree: additional low-severity
test-coverage & doc gaps (supplement to #38)"), subtask 4.5.12.4.

## Subtask text (as pulled from the issue body)

> **4.5.12.4 — NodeAllocator durability/consistency cross-check against
> existing nodes**
>
> - Acceptance criteria: `NodeAllocator` (`engine/btree/insert.go`) cross-checks
>   its restored high-water-mark against node IDs actually referenced/present
>   in the index file, so a lost/stale/recreated `.nodealloc` sidecar cannot
>   silently reissue already-used node IDs — mirroring
>   `engine/catalog/idalloc.go`'s `IDAllocator` cross-check pattern.
> - Test spec: `go test ./engine/btree/... -run TestNodeAllocatorCrossChecksExistingNodes`
> - Impacted modules: `engine/btree/insert.go`, `engine/btree/insert_test.go`

This is the exact gap flagged as an open, low-severity regression item during
subtask 1.2.3's verification (now re-numbered/re-scoped as issue #50's
4.5.12.4): see `.cdr/index/regression.jsonl` line for subtask "1.2.3"
(`engine/btree`, "NodeAllocator (insert.go) reintroduces the same
sidecar-state-file-loss/ID-reuse residual risk previously accepted as
non-blocking for engine/catalog/idalloc.go in 1.1.4 ... consider a
durability/consistency check ... cross-checking against the highest node ID
actually present ... before the persist/reload subtask (1.2.6) locks in the
on-disk contract").

## Context from adjacent work (must not duplicate/regress)

- Subtask 4.5.12.3 (commit d747925) already landed
  `TestLookupInternalNodeMultiKeyRouting` in `engine/btree/lookup_test.go`.
  Not touched by this subtask.
- Issue #41's commit bc08c0a added automatic `SaveRoot` calls in
  `Tree.Insert`/`Tree.propagate` (the *method* API) on bootstrap/root-split.
  This subtask's `NodeAllocator` is a different piece of the durability story
  (id allocation vs. root-pointer persistence) and must be purely additive:
  it does not touch `Tree.Insert`/`propagate`, only `NewNodeAllocator`.
- Subtask 4.5.12.6 (not yet dispatched) will append a SaveRoot-absent
  regression test to `engine/btree/insert_test.go` later. This subtask's own
  additions to `insert_test.go` must be clearly scoped/appended (own doc
  comment header, no interleaving) so that later addition can land cleanly.

## Acceptance criteria (restated, testable)

1. `NewNodeAllocator` cross-checks its restored `.nodealloc` high-water-mark
   against the highest node ID actually present/written in the underlying
   index file.
2. If the sidecar's high-water-mark is found to be behind (less than) the
   highest node ID actually present on disk, `NewNodeAllocator` returns a
   descriptive, non-nil error instead of silently allowing `Next()` to
   subsequently reissue an already-used node ID.
3. The reverse case (sidecar high-water-mark >= highest present node ID) is
   not an error — this is the normal case, exactly mirroring
   `engine/catalog/idalloc.go`'s `NewIDAllocator`/`maxFileIDInCatalog`
   asymmetry.
4. New test `TestNodeAllocatorCrossChecksExistingNodes` in
   `engine/btree/insert_test.go` exercises both the error case (stale/lost
   sidecar next to an index file with higher-numbered nodes already written)
   and the non-error case.
5. `go test ./engine/btree/... -run TestNodeAllocatorCrossChecksExistingNodes -v`
   passes; full `go test ./engine/btree/...` (and `-race`) remains green.
