# Architecture discovery

Token order followed: index/ -> memory/handoffs -> targeted LLD -> touched
files -> source.

Read `engine/btree/lookup.go` (396 lines) to find the actual routing branch:

- `descendToLeaf` (line 136-155, single-threaded free-function path used by
  `Lookup`):
  ```go
  i := sort.Search(len(internal.Keys), func(i int) bool { return path < internal.Keys[i] })
  currentID = internal.Children[i]
  ```
- `lookupOnce` (line 251-345, concurrency-safe path used by `Tree.Lookup`)
  has the identical shape at line ~293.

Both implementations compute `i` in `[0, len(internal.Keys)]` via
`sort.Search` and descend into `Children[i]`. The three qualitatively
distinct outcomes are:
  - `i == 0` (path sorts before every key — leftmost child)
  - `i == len(Keys)` (path sorts at/after every key — rightmost child)
  - `0 < i < len(Keys)` (path sorts strictly between two interior keys —
    a STRICTLY INTERIOR child, only possible when the node holds >= 2 keys)

Read `engine/btree/lookup_test.go`'s existing `buildTestTree` fixture: every
internal node it constructs (`int1ID`, `int2ID`, `rootID_`) has exactly ONE
key (`len(Keys) == 1`), so `i` can only ever be 0 or 1 (== len(Keys)) there —
the `0 < i < len(Keys)` branch is structurally unreachable via that fixture,
confirming the acceptance criteria's gap is real, not already covered.

Read `engine/btree/node.go`'s `InternalNode` struct (Keys, Children,
Version, NextSibling, LowKey) to confirm what a hand-built >=2-key internal
node needs: `len(Children) == len(Keys)+1`; `NextSibling`/`LowKey` default
correctly to zero-value (`noSibling`/`""`) for a childless-of-any-split node
used only in a standalone hand-built test fixture (mirrors buildTestTree's
existing convention).

Read `engine/btree/insert_test.go`'s `newTestStoreAndAllocator` and
`engine/btree/insert.go`'s `NewNodeAllocator`/`NewTree` signatures to wire up
`Tree.Lookup` against the same hand-built store (rather than only exercising
the free-function `Lookup`/`descendToLeaf` path), since the acceptance
criteria names `Tree.Lookup` specifically and `lookupOnce` is a materially
separate implementation of the same routing shape.
