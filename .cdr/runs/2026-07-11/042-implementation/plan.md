# Plan

Add `TestDeleteSpliceGj0CrossGrandparentNoDangling` to `engine/btree/delete_test.go`.

1. Hand-construct (bypassing Insert, same style as `TestDeleteSpliceFirstChildAncestorFixesNextSibling`)
   a 5-level tree (root -> level1 -> level2 -> level3(=ancestor's own level) -> leaves) shaped so that:
   - `ANC` (level3) is `G`'s (level2) FIRST child (`gj == 0`).
   - `G` is `L`'s (level1) FIRST child too (forces `findLeftNeighborAtSameLevel`'s walk-up loop to
     execute TWICE, i.e. `levelsUp == 2`, before it finds an ancestor that isn't a first child) —
     this is deeper/more-nested than the existing committed `levelsUp == 1` test and matches the
     specific gap flagged non-blocking in `007-verification`.
   - `L` is root's SECOND child, so root's first child `Lprev`'s rightmost spine (`Lprev -> Pb -> Qb`)
     is the true cross-grandparent left neighbor two levels up and two subtrees over.
   - Level-3 NextSibling chain before delete: `Qb -> ANC -> ANC2 -> noSibling`.
2. Delete two keys from `ANC`'s first leaf child so it empties, triggers a merge with the second
   leaf child, degenerates `ANC` to 1 child, and triggers `spliceOutDegenerateAncestor`.
3. Assert:
   - `G.Children` no longer references `ANC`'s node ID, still has exactly 2 entries.
   - Level-3 NextSibling chain, independently re-walked, is now `Qb -> ANC2 -> noSibling` (no
     dangling pointer to the abandoned `ANC` id).
   - `assertNoOrphanedPointers` / `assertStructuralInvariants` / `assertAllLookupable` all pass on
     the resulting tree.
4. Concurrent-descent clause: before/after the delete sequence, race a pool of goroutines calling
   `tr.Lookup` for keys spanning both subtrees against the goroutine performing `tr.Delete`, run
   under `-race`, and assert no error/panic and no incorrect miss for keys known-present throughout.

No production code changes planned (see architecture-discovery.md / impact-analysis.json for why).

## Self-consistency gate (this agent only; NOT verification)
- `go test ./engine/btree/... -race -run TestDeleteSpliceGj0CrossGrandparentNoDangling -v`
- `go test ./engine/btree/... -race -v`
- `go test ./engine/... -race`
- `gofmt -l engine/btree/delete_test.go` clean
