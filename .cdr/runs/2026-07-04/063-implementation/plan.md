# Plan — subtask 1.5.2

Add `TestStorageCoreCrashRecovery` to `engine/integration_test.go`.

1. Gen1 setup (mirrors 1.5.1's helper wiring): FileManager, Catalog,
   IDAllocator, wal.Writer, ContentStore, btree IndexFile + NodeStore +
   NodeAllocator, `rootNodeID` var + local `insertPath` closure.
2. Create 3 files under `topics/keep/` fully (Create + insertPath each);
   fully commit one `Append` on one of them.
3. `btree.SaveRoot(store1, rootNodeID)` — checkpoint the root reflecting all
   of the above.
4. Allocate (but never create/insert) one more fileID, for
   `topics/keep/crashed` — models an append that was interrupted before its
   WAL record ever completed.
5. Cleanly close every gen-1 handle; capture `w1.SegmentNum()` beforehand.
6. Reopen the active WAL segment file `O_APPEND` and inject a torn record
   (8-byte header claiming a large payload + a short partial payload),
   reusing the `TestCrashInjectionRecovery` "torn payload at tail" recipe.
7. Gen2 "restart": reopen FileManager/Catalog/IDAllocator/wal.Writer (which
   auto-truncates the torn tail)/ContentStore; call `catalog.RecoverFromWAL`;
   reopen btree IndexFile/NodeStore/NodeAllocator; `btree.LoadRoot`.
8. Assertions:
   - Each of the 3 committed files: `btree.Lookup` finds correct fileID;
     `cat2.Get` SizeBytes matches; `cs2.Read` byte-for-byte matches expected
     content (including the one committed Append).
   - `btree.PrefixScan(store2, root2, "topics/keep/")` returns exactly 3
     entries (not 4) — the crashed path is not indexed.
   - `btree.Lookup(store2, root2, "topics/keep/crashed")` -> not found.
   - `cat2.Get(crashedFileID)` -> `catalog.ErrNotFound`.
   - `os.Stat(cs2.ContentPath(crashedFileID))` -> `os.IsNotExist` (no
     partial/corrupted content file on disk at all).
   - Post-recovery usability: create one brand-new file via gen2 handles
     (Create + insertPath against store2/nodeAlloc2/root2) and read it back,
     proving the recovered system is fully live, not just read-consistent.

No production code changes anticipated (confirmed in architecture-discovery
— every needed primitive already exists and is already verified).
