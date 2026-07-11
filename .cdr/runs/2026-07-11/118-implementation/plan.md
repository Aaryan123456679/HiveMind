# Plan — subtask 4.5.4.1 (issue #41)

1. Confirm no production writer of `RecordBTreeInsert`/`RecordBTreeDelete`
   exists (repo-wide grep) — rules out the "wire into real replay-based
   reconstruction" alternative as in-scope for one commit.
2. Identify the exact two call sites in `engine/btree/insert.go` where
   `t.root` changes: `Tree.Insert`'s bootstrap branch, and `propagate`'s
   root-split branch.
3. Add an automatic `SaveRoot(store, t.root)` call at each site, immediately
   after `t.root` is reassigned and still under `rootMu`, propagating any
   error as a wrapped `fmt.Errorf` (matching this file's existing error-
   wrapping convention) rather than swallowing it.
4. Add doc comments on `Tree.Insert` explaining the new automatic-
   checkpointing behavior, its scope (only root-changing events, not every
   insert), and why that preserves `persist.go`'s original no-fsync-on-every-
   mutation design rationale.
5. Add `TestCrashBetweenInsertAndSaveRootRecovers` to `engine/btree/
   btree_test.go`: insert 400 sequential keys via `Tree.Insert` (forces
   bootstrap + at least one root split) with no manual `SaveRoot` call
   anywhere, close the file (simulated crash), reopen a fresh `NodeStore`
   over the same on-disk file, `LoadRoot`, and assert every key is still
   `Lookup`-able with its correct fileID via `assertAllLookupable`.
6. Run `gofmt -l` / `go vet` / `go build` scoped to `engine/btree` (avoiding
   an unrelated, untracked, in-flight `engine/engine_stress_test.go` package-
   name conflict from a concurrent agent that breaks `./engine/...` as a
   whole but does not touch `engine/btree` or `engine/wal`).
7. Run the full `-race` suite for `engine/btree`, `engine/wal`,
   `engine/catalog`, `engine/split`, `engine/mvcc` (every package that calls
   into btree's Insert/recovery paths) to confirm no regression.
8. Self-consistency check (build green, new test passes, full suite green,
   validation matrix covered) — NOT verification (deferred to `/cdr:verify`
   per invariant I4).
9. One local commit, Problem/Solution/Impact format, scoped to
   `engine/btree/insert.go` + `engine/btree/btree_test.go` only.
10. Handoff with pointers only, noting subtasks 4.5.4.2-4.5.4.5's scope from
    the issue checklist for the next dispatch.
