# Plan — 2b.3.3

1. Add `ExecuteSplitBtreeInsert(tree *btree.Tree, oldPath string, originalFileID uint64, newPathFileIDs map[string]uint64) error` to `engine/split/execute.go`:
   - Validate `tree != nil`, `oldPath != ""`, `len(newPathFileIDs) > 0`.
   - Validate every key in `newPathFileIDs` is non-empty and != oldPath (defensive: a new topic path must never collide with the path being redirected away from).
   - For each (newPath, newFileID) pair (iterate in a deterministic, sorted-by-key order so behavior/error messages are reproducible across runs despite Go map iteration order), call `tree.Insert(newPath, newFileID)`; wrap/return any error immediately.
   - Call `tree.Insert(oldPath, originalFileID)` as the explicit repoint step (see architecture-discovery.md for why this is correct/necessary even though it is an upsert no-op when the tree already holds oldPath->originalFileID).
   - Full doc comment matching the style of `ExecuteSplitAllocateAndWrite`/`ExecuteSplitRedirectStub`, cross-referencing 2b.3.1/2b.3.2 and this run's architecture-discovery.md.
2. Add `TestSplitBtreeRepoint` to `engine/split/execute_test.go`:
   - New helper `newTestBtree(t)` building a `*btree.Tree` via `btree.OpenIndexFile`/`btree.NewNodeStore`/`btree.NewNodeAllocator`/`btree.NewTree(store, alloc, 0)` in an isolated `t.TempDir()`.
   - Subtest "repoint": seed the tree with `oldPath -> originalFileID` (simulating pre-split state, since no repo code populates this yet), run `ExecuteSplitAllocateAndWrite` against `FixtureSplitPlan`/`FixtureFileContent` to get new fileIDs, call `ExecuteSplitBtreeInsert`, then assert via `tree.Lookup`:
     - old path (e.g. "fixture-original.md") still resolves to `originalFileID` (unchanged).
     - each new path resolves to its own new fileID (distinct from originalFileID and from each other).
   - Compose with 2b.3.2: also call `ExecuteSplitRedirectStub` (seeding a `StatusSplit` catalog record first) so that `originalFileID`'s content is the redirect stub, and assert reading `cs.ContentPath(lookedUpOldFileID)` yields the redirect-stub bytes (`buildRedirectStubContent`), while reading `cs.ContentPath(lookedUpNewFileID)` for a new path yields the actual split-off content (`extractSections`) -- proving end-to-end that old-path lookup now leads to stub content and new-path lookups lead to real content.
   - Subtest "nil_tree": `ExecuteSplitBtreeInsert(nil, ...)` errors.
   - Subtest "empty_old_path": errors.
   - Subtest "empty_new_paths": errors (nil/empty map).
   - Subtest "new_path_equals_old_path": errors (defensive guard).
3. Self-consistency: build/vet/gofmt clean; targeted test; full `./split/...` race suite; skip btree suite (btree/ package itself untouched, no source changes there -- but will still run it if time permits per instructions "if you touch engine/btree/").
4. One commit, Problem/Solution/Impact style.
5. handoff.json with pointers only.
