# Plan — Subtask 1.2.2

## 1. `engine/btree/lookup.go`

- `type NodeStore struct { f *os.File }`
- `func NewNodeStore(f *os.File) *NodeStore` — trivial constructor wrapping the
  `*os.File` returned by `OpenIndexFile`.
- `const rootReservedNodeID = 0` reuse concept from node.go's `noSibling`/page-0
  reservation comment (document real node IDs start at 1).
- `func (s *NodeStore) ReadNode(nodeID uint64) (isLeaf bool, leaf LeafNode, internal InternalNode, err error)`
  - reject `nodeID == 0` (reserved, never a valid node) with an error.
  - seek to `int64(nodeID) * NodeSize` via `ReadAt` (no manual Seek needed — use
    `ReadAt` for offset-based reads, avoids shared file-position races).
  - read exactly `NodeSize` bytes; on short read, error.
  - peek `data[0]` (nodeTypeLeaf/nodeTypeInternal) is package-private; instead call
    `DecodeLeafNode`/`DecodeInternalNode` and dispatch by trying to decode header
    type first via a small unexported peek, OR simplest: add a tiny unexported
    helper in lookup.go that reads first byte before deciding branch, since
    nodeTypeLeaf/nodeTypeInternal consts are already unexported package-level in
    node.go and visible to lookup.go (same package `btree`).
  - dispatch: if `data[0] == nodeTypeLeaf`, call `DecodeLeafNode`; if
    `nodeTypeInternal`, call `DecodeInternalNode`; else error.
- `func (s *NodeStore) WriteNode(nodeID uint64, encoded []byte) error`
  - reject `nodeID == 0`.
  - require `len(encoded) == NodeSize` (defensive; Encode() always produces this).
  - `WriteAt(encoded, int64(nodeID)*NodeSize)`.
- `func Lookup(store *NodeStore, rootNodeID uint64, path string) (fileID uint64, found bool, err error)`
  - loop: `ReadNode(currentID)`.
  - if internal: `i := sort.Search(len(node.Keys), func(i int) bool { return path < node.Keys[i] })`;
    `currentID = node.Children[i]`; continue loop.
  - if leaf: `i := sort.Search(len(node.Keys), func(i int) bool { return node.Keys[i] >= path })`;
    if `i < len(node.Keys) && node.Keys[i] == path` return `node.FileIDs[i], true, nil`;
    else return `0, false, nil`.
  - propagate any ReadNode error immediately.

## 2. `engine/btree/lookup_test.go`

- Package-level doc comment block above `buildTestTree` explicitly stating: this is
  test-only scaffolding to exercise Lookup, NOT subtask 1.2.3's real insert-with-
  splitting API; it hand-constructs a fixed, pre-balanced tree shape and does not
  perform any splitting/rebalancing logic.
- `buildTestTree(t *testing.T) (*NodeStore, uint64 rootID, wantPresent map[string]uint64, wantAbsent []string)`
  - Fixed sorted path set across 4 leaves, e.g.:
    - leaf1: "auth/login" -> 101, "auth/logout" -> 102
    - leaf2: "auth/oauth" -> 103, "auth/session" -> 104
    - leaf3: "billing/invoice" -> 201, "billing/plan" -> 202
    - leaf4: "search/index" -> 301, "search/query" -> 302
  - internal1 (covers leaf1,leaf2): Keys=["auth/oauth"], Children=[leaf1ID, leaf2ID]
  - internal2 (covers leaf3,leaf4): Keys=["billing/plan"], Children=[leaf3ID, leaf4ID]
  - root: Keys=["billing/invoice"], Children=[internal1ID, internal2ID]
  - NextLeaf chain: leaf1->leaf2->leaf3->leaf4->noSibling(0).
  - node IDs assigned 1..7 (root=1 by convention... simplify: assign IDs in the order
    leaves then internals then root, document the numbering in a comment; exact
    numeric IDs don't matter, only that they're all >=1 and consistent).
  - Write every node via `store.WriteNode`.
- `TestLookup(t *testing.T)`
  - build tree in `t.TempDir()`-based file via `OpenIndexFile` + `NewNodeStore`.
  - present cases: assert each of the 8 known paths above resolves via `Lookup` to
    its exact fileID, `found == true`, `err == nil` -- scattered across all 4 leaves.
  - absent-in-populated-leaf cases: e.g. "auth/middleware" (sorts within leaf1/leaf2
    range but isn't a key), "billing/refund" (sorts within leaf3's range) -- assert
    `found == false, err == nil, fileID == 0`.
  - boundary cases: a path before the first key overall ("aaa/first") and a path
    after the last key overall ("zzz/last") -- assert `found == false, err == nil`.
  - run under `-race` as part of the full package test.

## Addressing convention documented in lookup.go's doc comments

- nodeID * NodeSize byte offset; node ID 0 reserved/never valid (mirrors node.go's
  existing `noSibling`/page-0-reservation comments and catalog's `pageID * PageSize`
  precedent). No allocator/free-list in this subtask -- NodeStore is a pure
  read/write-by-ID accessor; nodes are placed at whatever ID the caller picks (the
  test helper simply lays them out sequentially in a temp file). A real allocator, if
  ever needed, is deferred to whichever later subtask's LLD calls for it.

## Self-consistency checks (not verification)

- `go build ./engine/...`
- `go vet ./engine/btree/...`
- `go test ./engine/btree/... -run TestLookup -race -v`
- `go test ./engine/btree/... -race -v` (full package, ensures no regression to 1.2.1's TestNodeSerialization)
