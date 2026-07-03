# Plan — Subtask 1.2.1

## `engine/btree/node.go`

1. Package-level constants:
   - `NodeSize = 4096` (fixed on-disk node size, mirrors `catalog.PageSize`).
   - `DefaultIndexFileName = "index/name.idx"` (conventional path, per LLD).
   - Node type discriminator: `nodeTypeLeaf byte = 0`, `nodeTypeInternal byte = 1`.
   - Fixed header offsets: `offNodeType = 0`, `offKeyCount = 1` (uint16), body starts at
     `offBody = 3`.

2. Types:
   - `type LeafNode struct { Keys []string; FileIDs []uint64; NextLeaf uint64 }`
     (`NextLeaf` = 0 sentinel meaning "no sibling"; real node/page IDs are allocated starting
     at 1 in later subtasks, consistent with catalog's page-0-reserved convention).
   - `type InternalNode struct { Keys []string; Children []uint64 }`
     (len(Children) == len(Keys)+1, except the degenerate 0-key/0-child case for an empty root).

3. Encoding scheme (both node types):
   - Byte 0: node type discriminator.
   - Bytes 1-2: uint16 key count (little-endian).
   - Then repeated per key: uint16 key-length prefix + raw key bytes (UTF-8, no NUL terminator).
   - Leaf-specific: after keys, one uint64 FileID per key (little-endian), then trailing uint64
     NextLeaf pointer.
   - Internal-specific: after keys, `len(Keys)+1` uint64 child pointers (little-endian).
   - Compute total required size before writing; if it exceeds `NodeSize`, return an error
     (never truncate), matching `catalog/record.go`'s precedent.
   - `Encode()` always returns an exactly `NodeSize`-byte buffer (zero-padded) so on-disk records
     are fixed-width and directly addressable by node index * NodeSize.

4. Functions:
   - `func (n LeafNode) Encode() ([]byte, error)`
   - `func DecodeLeafNode(data []byte) (LeafNode, error)`
   - `func (n InternalNode) Encode() ([]byte, error)`
   - `func DecodeInternalNode(data []byte) (InternalNode, error)`
   - `func OpenIndexFile(path string) (*os.File, error)` — `os.OpenFile(path,
     os.O_RDWR|os.O_CREATE, 0o644)`, creating parent dir is NOT this function's job (caller's
     responsibility / left for later subtask that wires up the real index directory); the
     acceptance criterion is about the *file* being created on first use given a valid path.
   - A shared `decodeHeader(data []byte) (nodeType byte, keyCount int, err error)` helper and a
     shared `decodeKeys` helper to avoid duplicating the key-list-decode loop between Leaf and
     Internal decoders.

5. Error handling: hard errors (fmt.Errorf) for:
   - encoded size > NodeSize.
   - buffer too short during decode (any read past `len(data)`).
   - wrong node-type discriminator when decoding into the wrong decoder (e.g. `DecodeLeafNode`
     given internal-node bytes) or an unrecognized discriminator byte.
   - internal node with `len(Children) != len(Keys)+1`.

## `engine/btree/node_test.go`

- `TestNodeSerialization` (matches required test spec `-run TestNodeSerialization`), with
  subtests:
  - Leaf round-trip: 0 keys, 1 key, many keys (well under capacity) — assert
    `reflect.DeepEqual`/manual field equality after `Decode(Encode(node))`.
  - Internal round-trip: 0 keys (single child, degenerate root), 1 key, many keys.
  - Overflow rejection: build a leaf/internal node whose keys+values would exceed `NodeSize`,
    assert `Encode()` returns a non-nil error (not a truncated buffer).
  - File-created-on-first-use: `OpenIndexFile` on a `t.TempDir()`-based path that does not yet
    exist; assert no error, assert `os.Stat` on the path succeeds afterward, and calling it again
    on the same path doesn't error or destroy existing content (open is idempotent-safe).

## Self-consistency checks (internal only, not verification)
- `go build ./engine/...` from `engine/` module dir.
- `go test ./engine/btree/... -run TestNodeSerialization -race -v`.
- Manually cross-check validation-matrix.json rows are all covered by subtests above.
