# Architecture Discovery — Subtask 1.2.1

## Sources read (in order)
1. `.cdr/memory/state.md`, `decisions.md`, `pending.md`, `impact-map.md`, `timeline.md`,
   `regression-routes.md` — all effectively empty/no prior entries relevant to `engine/btree`.
2. `docs/HLD.md` (system context) — confirms `engine/btree` is the on-disk index mapping topic
   paths to catalog `fileID`s, sits alongside `engine/catalog`, `engine/split`, `engine/wal`.
3. `docs/LLD/btree.md` (read in full) — purpose: custom on-disk B+Tree persisted at
   `index/name.idx`, mapping topic path strings -> fileIDs (source of truth in
   `engine/catalog`). Ops: point lookup, prefix scan, insert, delete. Concurrency: latch-crabbing
   writes, optimistic lock-free reads w/ version-counter retry (out of scope for this subtask —
   no concurrency to implement yet, node encode/decode is a pure data-transform). No explicit
   page/node size or byte-layout guidance given in the LLD — left to implementation judgment,
   consistent with existing conventions.
4. `.cdr/index/file.jsonl`, `.cdr/index/task.jsonl` — confirms `engine/btree` currently only has
   a `doc.go` placeholder (from the 2026-07-03-001-documentation run). No prior btree subtask has
   been implemented. `engine/catalog` (5/5 subtasks) is the established convention reference.

## Established conventions found in `engine/catalog` (reference package, all verified)
- `record.go`: fixed-offset little-endian encoding via `binary.LittleEndian`, explicit
  `Encode() ([]byte, error)` / `Decode(data []byte) (T, error)` pair. Hard-errors (does not
  truncate) when data exceeds a fixed capacity (`MaxRedirectTargets`) — this is an explicit
  precedent following 1.1.1's verification finding, called out in the task instructions.
- `page.go`: `PageSize = 4096` fixed page size; slotted-page directory pattern for variable-length
  records within a fixed-size page — the natural precedent for how to fit variable-length topic
  path strings into a fixed-size B+Tree node.
- `file.go`: `os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)` is the established idiom for
  "create file on first use". `DefaultCatalogFileName` constant + explicit test-time
  `t.TempDir()` isolation (tests must never touch the real conventional path) is the established
  test-isolation convention.
- Module path: `github.com/Aaryan123456679/HiveMind/engine` (single Go module under `engine/`).

## Design decisions for `engine/btree/node.go` (this subtask)
- **Node size**: fixed `NodeSize = 4096` bytes, mirroring `catalog.PageSize`, for consistency
  across the engine's on-disk formats (both are "one fixed-size unit read/written per disk op").
- **Two node types**: `LeafNode` (keys + fileID values + `NextLeaf` sibling pointer for future
  1.2.5 prefix/range scans) and `InternalNode` (keys + child pointers, one more child than keys).
- **Variable-length keys**: topic path strings are not fixed-width like catalog's uint64 fields,
  so the node body uses a length-prefixed (uint16 length + raw bytes) encoding per key, packed
  sequentially after a fixed header. This is the natural extension of catalog's fixed-offset
  convention to variable-length data (analogous to `page.go`'s slotted design, but simpler since
  ordering is implicit/sorted and no delete/tombstone tracking is needed for this subtask).
- **Capacity check, not truncation**: `Encode()` computes the required byte length up front and
  returns an error if it exceeds `NodeSize`, per the explicit 1.1.1 precedent instruction — never
  silently truncates.
- **File creation helper**: a small `OpenIndexFile(path string) (*os.File, error)` using the same
  `os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)` idiom as `catalog/file.go`, satisfying the
  "index/name.idx file is created on first use" acceptance criterion without building out a full
  FileManager/paged-store (deferred; not required by this subtask's test spec).

## Files touched
- New: `engine/btree/node.go`, `engine/btree/node_test.go`.
- No existing files modified (package was previously only `doc.go`).
