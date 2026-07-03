# Plan: 1.2.1 follow-up — reserve version-counter field

## Requirement
`docs/LLD/btree.md` (Concurrency > Reads) requires optimistic lock-free reads that check
"its version counter is unchanged" and retry otherwise. Subtask 1.2.1's committed on-disk
node format (`engine/btree/node.go`) has no header space reserved for this counter. Adding
it now (format is new, only exercised by unit tests) avoids a breaking on-disk migration
once 1.2.2-1.2.5 build on top of the format.

## Architecture discovery
- Header layout before fix: `offNodeType`(1 byte) + `offKeyCount`(2 bytes, uint16) = 3
  bytes total (`offBody = 3`).
- `LeafNode`/`InternalNode` structs, `Encode()`/`Decode*()`, `decodeHeader()`,
  `leafEncodedSize()`/`internalEncodedSize()` all key off `offBody`, so widening the
  header only requires changing the offset constants plus one write/read call in each
  Encode/Decode path — the size-accounting functions (`size := offBody`) pick up the new
  width automatically.

## Impact analysis
- Touches: `engine/btree/node.go` (struct fields, offsets, encode/decode), `node_test.go`
  (round-trip cases + one new subtest).
- No other files reference the header layout or `offBody`/`offKeyCount` (checked via grep
  across `engine/`); `OpenIndexFile` is untouched (file-level open, not node encoding).
- Downstream subtasks (1.2.2-1.2.5) do not exist yet, so no call-site breakage.

## Plan
1. Add `offVersion = offKeyCount + 2` (uint64, 8 bytes) between key-count and body;
   `offBody = offVersion + 8` = 11 bytes total header.
2. Add `Version uint64` field to `LeafNode` and `InternalNode`.
3. `Encode()`: write `binary.LittleEndian.PutUint64(buf[offVersion:], n.Version)` in both.
4. `decodeHeader()`: return a fourth value, `version uint64`, read from `data[offVersion:]`.
5. `DecodeLeafNode`/`DecodeInternalNode`: thread `version` through into the returned struct.
6. Tests: set `Version` to a non-zero value in one leaf case and one internal case of the
   round-trip table (round-trip equality already covers the field via `reflect.DeepEqual`
   since `normalizeLeaf`/`normalizeInternal` don't touch `Version`).
7. Add a small "decode rejects mismatched node type" subtest: encode a leaf, feed the bytes
   to `DecodeInternalNode` (expect error), and vice versa with `DecodeLeafNode` on an
   internal-tagged buffer.

## Validation matrix
| Case | Covered by |
|---|---|
| Leaf round-trip incl. non-zero Version | `leaf round-trip` case 3 |
| Internal round-trip incl. non-zero Version | `internal round-trip` case 3 |
| Zero-value Version still round-trips | `leaf`/`internal` cases 1-2 (Version left at zero-value) |
| Overflow rejection unaffected by header width change | `overflow rejected` subtest (unchanged, still passes) |
| Cross-type decode rejected | new `decode rejects mismatched node type` subtest |
| File creation semantics unaffected | `file created on first use` subtest (unchanged) |

## Self-consistency (internal only — not verification)
- `go build ./engine/...` — pass
- `go vet ./engine/btree/...` — pass
- `go test ./engine/btree/... -run TestNodeSerialization -race -v` — pass (5/5 subtests)
- `go test ./engine/btree/... -race -v` — pass (full package, same 5 subtests; no other
  test files in package yet)
