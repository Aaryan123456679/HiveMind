# Plan — Subtask 1.2.6

1. Add `engine/btree/persist.go`:
   - `rootStateSuffix = ".root"`, `rootStateSize = 8` constants.
   - `SaveRoot(store *NodeStore, rootNodeID uint64) error`: derive sidecar path from
     `store.f.Name() + rootStateSuffix`; open with `O_RDWR|O_CREATE`; encode `rootNodeID` as
     8-byte LE; `WriteAt` at offset 0; `Sync`; `Close`; wrap all errors with `btree:` prefix
     consistent with existing style.
   - `LoadRoot(store *NodeStore) (uint64, error)`: derive same sidecar path; `os.Open` (read-only,
     no create); if `os.IsNotExist(err)`, return `(reservedNodeID, nil)`; else propagate open
     error; `Stat` and validate size is exactly `rootStateSize` (0 also acceptable defensively? —
     no: an existing-but-empty file is unexpected/corrupt since SaveRoot always writes exactly 8
     bytes atomically via one WriteAt, so treat size-not-equal-8 as an error, mirroring
     NewNodeAllocator's stricter "0 or exact size" pattern is NOT applicable here since the file
     wouldn't exist at all if SaveRoot was never called — decide: accept only exactly
     rootStateSize, else error); `ReadAt` 8 bytes; decode LE uint64; `Close`; return.
2. Add `engine/btree/btree_test.go`:
   - `TestPersistReload`: build tree via real `Insert` (reuse `newTestStoreAndAllocator`-style
     setup but with an explicit, retained path via `filepath.Join(t.TempDir(), "name.idx")` so the
     same on-disk file can be reopened), insert enough keys to force multiple splits (reuse
     `genKey`-style key generation, ~300-400 keys), call `SaveRoot`, close the `*os.File` and the
     allocator's sidecar file explicitly (not via t.Cleanup, so the "close" actually happens before
     reopen), record expected Lookup results and PrefixScan results for a few prefixes from the
     pre-close tree, then: open a **brand new** `*os.File` via `OpenIndexFile` on the same path,
     wrap in a **brand new** `NodeStore`, call `LoadRoot` to recover the root ID, and assert
     `Lookup` for every originally-inserted key and `PrefixScan` for the same prefixes return
     identical results to the pre-close values.
   - `TestLoadRootFreshIndexFile`: open a brand-new, never-persisted-to index file, call `LoadRoot`
     directly, assert it returns `(reservedNodeID, nil)` with no error (not a crash).
3. Update `.cdr/index/file.jsonl`: add entries for `engine/btree/persist.go` and
   `engine/btree/btree_test.go`.
4. Update `.cdr/index/task.jsonl`: add `task-1.2.6` with `state: implemented`, run ID, commit SHA
   (filled in after actual commit).
5. Self-consistency: `go build ./engine/...`, `go vet ./engine/...`,
   `go test ./engine/btree/... -race -v -count=1` (all prior + new tests green).
6. One local commit (no push), Problem/Solution/Impact format.
7. Write `handoff.json` with pointers only.

## Non-goals (explicitly out of scope, per issue text + design guidance)
- Wiring `SaveRoot` into `Insert`/`Delete` themselves.
- Any WAL/crash-consistency guarantee beyond "SaveRoot completed before process exit" (that is
  `wal/`'s concern per docs/HLD.md's roadmap).
- Updating `docs/LLD/btree.md` (left to the documentation/LLD-sync agent).
