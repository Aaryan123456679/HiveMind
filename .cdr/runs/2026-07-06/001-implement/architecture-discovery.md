# Architecture Discovery — Subtask 3.1.1

## Existing docs consulted

- `docs/HLD.md`: confirms `graph/` = "Adjacency store + traversal API over topic/file
  relationships"; graph traversal is capped `k + 2k` files system-wide (query-agent concern, not
  this subtask).
- `docs/LLD/graph.md` (status: "scaffold only" as of `edge_append.go`'s landing, last_synced
  699105b): already sketches the intended storage layout — `graph.dat`: "CSR-like compact
  adjacency arrays per source fileID, with periodic compaction"; writes are append-only per-node
  edge logs (future 3.1.2) to avoid locking a shared adjacency array. Edge shape:
  `{targetFileID, edgeType, weight, lastUpdated}` — note LLD's edge shape includes `weight` and
  `lastUpdated`, which the existing `edge_append.go` Edge struct (Source/Target/Type only) does
  NOT have — those fields matter for the CSR array (ENTITY_COOCCUR weight increments are a 3.1.3
  compaction concern) but 3.1.1 must decide whether to carry them in the CSR record now to avoid
  a breaking format change in 3.1.3. Decision: include them now (see plan.md) since the LLD
  already establishes the edge shape has weight+lastUpdated, and it's essentially free to add two
  more fixed-width fields to a not-yet-shipped binary format versus a later on-disk migration.
- `.cdr/index/file.jsonl`, `task.jsonl`: confirm `docs/LLD/graph.md` is the only graph-specific
  LLD entry, last touched in a documentation-only run; no prior CSR-specific design record exists.
  `task-2b.3.4` (graph edge-append primitive) is the only prior `engine/graph` implementation
  task; its notes flag that edge-append durability is NOT yet integrated into any WAL-covered
  crash-recovery replay path — noted as context, not applicable to 3.1.1 since CSR here uses its
  own self-contained atomic file format, not WAL replay.

## Source read directly

- `engine/graph/edge_append.go` (full read): defines `Edge{Source, Target uint64; Type EdgeType}`,
  `EdgeType` (`EdgeTypeInvalid=0`, `EdgeSplitSibling`, `EdgeRedirect`), fixed-width little-endian
  encode/decode (`edgeEncodedSize = 8+8+1 = 17` bytes), `EdgeAppender` wrapping
  `wal.OpenWriter`/`wal.Writer.Append` (fsync-per-append, WAL segment framing/CRC), `ReadAll`
  scanning `wal-<N>.log` segments in order. This is a pure append-only log — no offset index, no
  per-node grouping, no compaction. Confirms this is NOT the CSR format and doesn't need to be
  reused/extended for 3.1.1; only its *conventions* (little-endian fixed-width encode, CRC32
  IEEE, `fmt.Errorf("graph: ...: %w", err)` wrapping style, `EdgeType` byte enum with `String()`)
  carry over.
- `engine/wal/writer.go` (record framing, lines ~15-30, ~240-250, ~380-400): record header is
  `[0:4] uint32 LE payload length`, `[4:8] uint32 LE CRC32(IEEE) of payload`, followed by payload
  bytes; CRC mismatch on a full-length record is a hard corruption error, distinct from a torn
  tail (truncated final record, tolerated as "not yet fsynced" during recovery). This
  header+CRC32 convention is this repo's established durability primitive and is reused for
  `graph.dat`'s own header.
- `engine/catalog/content.go:528-563` (`writeContentFile`): the repo's established
  whole-file atomic-write convention for content that is NOT an append-only log: write to a
  temp sibling file in the same directory (`os.CreateTemp(dir, "<name>.tmp")`), `Write`, `Sync`,
  `Close`, then `os.Rename(tmpPath, finalPath)` (atomic on the same filesystem), with cleanup
  (`os.Remove(tmpPath)`) on any error path. This is the correct precedent for `graph.dat` (a
  single reloadable snapshot file, not a segmented append log like `wal`/`edge_append.go`), and
  is what subtask 3.1.1's `csr.go` follows for its `WriteCSR`/save path.

## Conclusion feeding into plan.md

`graph.dat` is a single-file, whole-snapshot binary format (not a WAL-style segmented log),
because CSR arrays are inherently a compacted, rewrite-the-whole-thing-on-update structure (per
LLD: periodic compaction target, not an incremental append target — that's the edge log's job).
Therefore the correct durability precedent to follow is `catalog/content.go`'s
temp-file+fsync+rename pattern, not `engine/wal`'s segment-rotation log — while still reusing
`engine/wal`'s CRC32 IEEE + fixed-width little-endian header conventions for internal record
integrity, since that is this repo's established checksum idiom (also mirrored in
`edge_append.go`).
