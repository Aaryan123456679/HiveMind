# Architecture discovery — subtask 1.5.2

## Sources read (index-first order)
- `.cdr/index/task.jsonl` (1.4.1-1.4.4, 1.5.1 history/commits)
- `engine/integration_test.go` (1.5.1, verified) — establishes the
  FileManager/Catalog/IDAllocator/wal.Writer/ContentStore/btree
  (IndexFile+NodeStore+NodeAllocator+rootNodeID var) wiring convention, and
  the `insertPath` closure pattern (`wal.AppendAndApply(w, wal.NewBTreeInsertRecord(path,fileID), func() error { ... btree.Insert ...})`).
- `engine/catalog/recovery.go` — `RecoverFromWAL(cat *Catalog, walDir string)`:
  replays `RecordCatalogPut`/`RecordCatalogDelete` only; explicitly a no-op
  (not an error) for any other record type (e.g. B+Tree records), by design
  ("this function's job is reconstructing Catalog state specifically").
- `engine/wal/recovery.go` — `Replay(dir, apply)`: reads checkpoint (none set
  in this test => replays from segment 0/offset 0), decodes every record,
  validates its type, and for the highest-numbered (last) segment silently
  discards a torn tail (truncated header or truncated payload) rather than
  erroring; a torn tail in a non-last segment, or a full-length record with a
  bad CRC, is a hard error.
- `engine/wal/writer.go` `OpenWriter` — on resume (existing segment file
  found) it independently re-validates and truncates any torn tail on the
  segment file itself *before* accepting further appends, so a resumed
  Writer's on-disk state and a fresh `Replay`'s view always agree.
- `engine/wal/recovery_test.go` `TestCrashInjectionRecovery` (1.3.5) — the
  established "simulated crash" idiom: after cleanly closing a Writer,
  reopen the last segment file `O_APPEND`, write a synthetic record header
  (4-byte LE length + 4-byte LE CRC) claiming a payload, then write fewer
  payload bytes than declared, then close. This reliably produces a torn
  tail Replay/OpenWriter both discard cleanly. Reused verbatim (header
  layout: offset 0 = length uint32, offset 4 = CRC uint32, both
  little-endian; segment file name `wal-<N>.log` — `writer.SegmentNum()` is
  exported and gives `<N>`, avoiding any dependency on unexported
  `segmentFilePrefix`/`segmentPath`).
- `engine/catalog/content_test.go` `TestContentDurabilityRestart` (1.4.4) —
  the established "restart" idiom: open brand-new Catalog/IDAllocator/
  wal.Writer/ContentStore against the same directory, call
  `catalog.RecoverFromWAL`, then read back. No genuine crash injection there
  (clean restart only) — 1.5.2 adds the torn-tail injection on top of this
  same restart shape.
- `engine/catalog/record.go` `CatalogRecord` — **does not store the topic
  path**, only `PathHash` (a lossy uint64 hash). This matters: the recovered
  Catalog alone cannot answer "what topic path did fileID N have", so a
  btree-rebuild-from-catalog design (as speculated in the task brief) is not
  actually possible with existing APIs.
- `engine/btree/persist.go` `SaveRoot`/`LoadRoot` — btree already has its
  own dedicated on-disk persistence/recovery story, entirely independent of
  WAL replay: `NodeStore.WriteNode` durably writes every node's bytes
  directly into the index file at the time of `Insert`/`Delete` (no
  batching), and a small `.root` sidecar file (written via `SaveRoot`,
  read via `LoadRoot`) durably persists the tree's current root node ID.
  Per persist.go's own doc comment, `SaveRoot` is deliberately NOT called
  automatically by `Insert`/`Delete` — the **caller** decides when to
  checkpoint the root (e.g. after a batch, or before a controlled shutdown).
  `LoadRoot` returns `reservedNodeID` (0), not an error, if `SaveRoot` was
  never called for this index file.
- `engine/btree/insert.go` `NodeAllocator` — also durably persists its own
  high-water-mark sidecar (`.nodealloc`) via WriteAt+Sync on every `Next()`,
  so reopening `NewNodeAllocator` against the same index file resumes
  correctly with no extra recovery step needed.
- `docs/LLD/btree.md` — a short scaffold-level doc; does not further specify
  a WAL-driven recovery story for btree, consistent with the above: btree's
  recovery is self-contained (its own on-disk node file + root/alloc
  sidecars), not WAL-replay-driven.

## Key finding: btree does NOT need "rebuild from catalog"
btree is NOT purely in-memory-per-process: `NodeStore.WriteNode` durably
writes to the index file on every `Insert`, and `SaveRoot`/`LoadRoot`
durably persists/recovers the root pointer across a restart. So the correct
"restart" pattern for btree, matching persist.go's documented design, is:
  1. (pre-crash) after every btree mutation the test wants to survive a
     restart, call `btree.SaveRoot(store, rootNodeID)` to checkpoint the
     current root — exactly the "caller decides when to checkpoint"
     contract persist.go documents.
  2. (post-restart) reopen the same index file, `NewNodeStore`,
     `NewNodeAllocator` (resumes its own sidecar automatically), and
     `btree.LoadRoot(store)` to recover the checkpointed root node ID.
No WAL replay of `RecordBTreeInsert`/`RecordBTreeDelete` is needed or used
for btree recovery in this test; those WAL records exist for other
subtasks'/future callers' use (e.g. audit/rebuild tooling), not because
btree's own recovery depends on them. This is called out explicitly in
handoff.json since it contradicts a plausible-sounding but incorrect
alternative (rebuilding paths from Catalog, which cannot work: Catalog only
has PathHash, never the literal path string).

## No new production code needed
Every primitive required (`catalog.RecoverFromWAL`, `wal.OpenWriter`'s
torn-tail auto-truncation, `btree.SaveRoot`/`LoadRoot`,
`btree.NodeAllocator`'s own sidecar resume) already exists and is already
verified from prior subtasks. This subtask is a pure test addition to
`engine/integration_test.go`; no `engine/catalog`, `engine/btree`, or
`engine/wal` production file changes.

## Simulated "crash mid-append" design
1. Generation 1: build catalog+idAlloc+wal.Writer+ContentStore+btree exactly
   as 1.5.1 does. Create several committed files (Create + btree insertPath),
   perform one fully-committed `ContentStore.Append` on one of them, then
   `btree.SaveRoot` to checkpoint the root.
2. Allocate one more fileID (`idAlloc1.Next()`) intended for a file that
   never actually gets created — modeling a `ContentStore.Create`/`Append`
   call that was interrupted before its WAL record was even durably written.
   No `cs1.Create`/`insertPath` call is made for it (mirrors the
   WAL-before-apply invariant: if the WAL write itself never completed, the
   apply step — content file write + catalog Put / btree Insert — never ran
   either, so nothing about this fileID should be observable anywhere after
   recovery).
3. Cleanly close all gen-1 handles, then reopen the WAL's active segment
   file directly (`O_APPEND`) and inject a torn record (full 8-byte header
   claiming a large payload length, followed by fewer payload bytes than
   declared) — the exact technique `TestCrashInjectionRecovery` ("torn
   payload at tail") uses in `engine/wal/recovery_test.go`.
4. Generation 2 ("restart"): reopen FileManager/Catalog/IDAllocator/
   wal.Writer (which auto-truncates the torn tail)/ContentStore, call
   `catalog.RecoverFromWAL`, reopen the btree index file/NodeStore/
   NodeAllocator, and `btree.LoadRoot`.
5. Assert: every committed pre-crash file is found and cross-checks
   consistently across btree -> catalog -> content (including the earlier
   full `Append`); the crashed fileID/path is absent from btree Lookup,
   catalog Get (`ErrNotFound`), PrefixScan, and has no content file at all
   on disk (`os.Stat` -> `IsNotExist`) — i.e. no partial/corrupted file is
   visible; and the recovered system is still fully usable (a brand-new
   post-recovery Create+Insert succeeds and round-trips).
