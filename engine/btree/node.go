package btree

import (
	"encoding/binary"
	"fmt"
	"os"
)

// NodeSize is the fixed size, in bytes, of every on-disk B+Tree node. This mirrors
// catalog.PageSize's role: the exact number of bytes read/written per node, so that a
// node index maps directly to a byte offset (index * NodeSize) within the index file.
// See docs/LLD/btree.md.
const NodeSize = 4096

// DefaultIndexFileName is the conventional repo-relative path callers should use for the
// on-disk B+Tree index file (see docs/LLD/btree.md). Tests must not use this constant
// directly for I/O; they should use an isolated t.TempDir() path instead so parallel test
// runs and CI never collide or leave stray artifacts on disk.
const DefaultIndexFileName = "index/name.idx"

// Node type discriminators, stored as the first byte of every encoded node.
const (
	nodeTypeLeaf     byte = 0
	nodeTypeInternal byte = 1
)

// Fixed byte offsets/widths for the shared node header. All multi-byte integers are
// little-endian, matching engine/catalog's on-disk encoding convention.
const (
	offNodeType = 0
	offKeyCount = offNodeType + 1 // uint16
	// offVersion holds an optimistic-concurrency version counter (see the
	// "Reads" section of docs/LLD/btree.md: readers check that a node's
	// version counter is unchanged and retry if it changed during the read).
	// This subtask only reserves and (de)serializes the field; the actual
	// CAS/atomic version-bump logic used by concurrent readers/writers is
	// implemented by a later, concurrency-focused subtask.
	offVersion = offKeyCount + 2 // uint64
	offBody    = offVersion + 8
)

// noSibling is the sentinel NextLeaf value meaning "this is the rightmost leaf; no
// sibling". Real node/page IDs are allocated starting at 1 by later subtasks, mirroring
// engine/catalog's convention that ID/page 0 is reserved rather than a valid data unit.
const noSibling uint64 = 0

// LeafNode is a B+Tree leaf: it stores the actual key -> fileID mappings, plus a pointer
// to the next leaf in sorted order (NextLeaf) so a future prefix/range scan (subtask
// 1.2.5) can walk leaves left-to-right without re-descending the tree. Keys must be kept
// sorted ascending by the caller; encoding/decoding does not itself sort or validate
// order (that is the responsibility of the insert/lookup logic in later subtasks).
type LeafNode struct {
	// Keys holds the topic path strings stored in this leaf (e.g. "auth/login").
	Keys []string
	// FileIDs holds the catalog fileID corresponding to each entry in Keys, at the same
	// index. len(FileIDs) must equal len(Keys).
	FileIDs []uint64
	// NextLeaf is the node index of the next leaf in sorted key order, or noSibling (0)
	// if this is the rightmost leaf.
	NextLeaf uint64
	// Version is an optimistic-concurrency version counter (see docs/LLD/btree.md's
	// "Reads" section). This subtask only stores/round-trips the field; the atomic
	// bump-on-write / unchanged-check-on-read logic belongs to a later, concurrency-
	// focused subtask.
	Version uint64
}

// InternalNode is a B+Tree internal (non-leaf) node: it stores separator keys and child
// pointers only, no values. For n keys there are always n+1 children (Children[i] holds
// all keys < Keys[i] for i==0, keys in [Keys[i-1], Keys[i]) for interior children, and
// keys >= Keys[n-1] for the last child), except the degenerate empty-root case of 0 keys
// and a single child.
type InternalNode struct {
	// Keys holds the separator topic path strings.
	Keys []string
	// Children holds the node index of each child. len(Children) must equal
	// len(Keys)+1.
	Children []uint64
	// Version is an optimistic-concurrency version counter (see docs/LLD/btree.md's
	// "Reads" section). This subtask only stores/round-trips the field; the atomic
	// bump-on-write / unchanged-check-on-read logic belongs to a later, concurrency-
	// focused subtask.
	Version uint64
	// NextSibling is the node index of this internal node's right sibling at the
	// same tree level, or noSibling (0) if this is the rightmost node at its level.
	// Mirrors LeafNode.NextLeaf's role, and exists for the same reason a Blink-tree
	// keeps right-links at every level, not just leaves: subtask 2a.4.2's
	// latch-crabbing insert releases each ancestor's latch before a split it may be
	// about to cause has been propagated up to it, so a concurrent writer following
	// a momentarily-stale parent pointer can land on a node whose upper key range
	// has already been split off into a new right sibling. NextSibling lets that
	// writer detect the overshoot (its target key is greater than every key
	// currently in this node) and "move right" to find the correct, already-split
	// node instead of silently inserting into the wrong place -- see insert.go's
	// crabInsert/findParent for the move-right recovery logic that reads this field.
	// Set alongside NextLeaf-style chaining whenever an internal node splits
	// (propagateSplit/Tree.propagate); zero-valued (noSibling) for a brand-new root
	// or any node that has never had a right sibling.
	NextSibling uint64
	// LowKey is the smallest key reachable anywhere within this node's subtree,
	// or "" if this node is the leftmost node at its level (no lower bound).
	// This is deliberately NOT the same value as Keys[0]: Keys[0] is a separator
	// one level further down the tree (the boundary between this node's own
	// first two children), whereas LowKey is the boundary between THIS node and
	// its own left sibling, which is fixed forever once the node is created by a
	// split (promoted keys are never revised) and is otherwise unrelated to
	// Keys[0]. crabInsert/findParent's move-right recovery (see NextSibling)
	// peeks at a candidate right sibling's LowKey -- not its Keys[0] and not its
	// currently-populated max key -- to decide whether a target key genuinely
	// belongs there; using anything else under-corrects or over-corrects
	// whenever the sibling's occupied key range has gaps (e.g. concurrent,
	// out-of-order inserts), which caused exactly this kind of silent
	// misrouting during 2a.4.2's development. Set once at creation: the left
	// half of a split keeps its original LowKey unchanged; the right half's
	// LowKey becomes the promoted key. Empty ("") for a brand-new root and for
	// every node descended purely through Children[0] chains from it.
	LowKey string
}

// leafEncodedSize returns the number of bytes Encode would need to write n's contents,
// not counting any trailing zero padding up to NodeSize.
func leafEncodedSize(n LeafNode) int {
	size := offBody
	for _, k := range n.Keys {
		size += 2 + len(k) // uint16 length prefix + key bytes
	}
	size += 8 * len(n.FileIDs) // one uint64 fileID per key
	size += 8                  // trailing NextLeaf pointer
	return size
}

// internalEncodedSize returns the number of bytes Encode would need to write n's
// contents, not counting any trailing zero padding up to NodeSize.
func internalEncodedSize(n InternalNode) int {
	size := offBody
	for _, k := range n.Keys {
		size += 2 + len(k)
	}
	size += 8 * len(n.Children)
	size += 8                 // trailing NextSibling pointer
	size += 2 + len(n.LowKey) // trailing length-prefixed LowKey
	return size
}

// Encode serializes the leaf node into a new NodeSize-byte buffer. It returns an error,
// rather than silently truncating, if the node's contents (keys + fileIDs + sibling
// pointer) do not fit within NodeSize, and if len(FileIDs) != len(Keys).
func (n LeafNode) Encode() ([]byte, error) {
	if len(n.FileIDs) != len(n.Keys) {
		return nil, fmt.Errorf("btree: leaf node FileIDs count %d does not match Keys count %d", len(n.FileIDs), len(n.Keys))
	}
	if len(n.Keys) > 0xFFFF {
		return nil, fmt.Errorf("btree: leaf node key count %d exceeds uint16 range", len(n.Keys))
	}

	needed := leafEncodedSize(n)
	if needed > NodeSize {
		return nil, fmt.Errorf("btree: encoded leaf node size %d exceeds NodeSize %d (too many/too-large keys for one node)", needed, NodeSize)
	}

	buf := make([]byte, NodeSize)
	buf[offNodeType] = nodeTypeLeaf
	binary.LittleEndian.PutUint16(buf[offKeyCount:], uint16(len(n.Keys)))
	binary.LittleEndian.PutUint64(buf[offVersion:], n.Version)

	off := offBody
	off = encodeKeys(buf, off, n.Keys)
	for _, id := range n.FileIDs {
		binary.LittleEndian.PutUint64(buf[off:], id)
		off += 8
	}
	binary.LittleEndian.PutUint64(buf[off:], n.NextLeaf)

	return buf, nil
}

// Encode serializes the internal node into a new NodeSize-byte buffer. It returns an
// error, rather than silently truncating, if the node's contents (keys + children) do
// not fit within NodeSize, and if len(Children) != len(Keys)+1.
func (n InternalNode) Encode() ([]byte, error) {
	if len(n.Children) != len(n.Keys)+1 {
		return nil, fmt.Errorf("btree: internal node Children count %d does not equal Keys count %d + 1", len(n.Children), len(n.Keys))
	}
	if len(n.Keys) > 0xFFFF {
		return nil, fmt.Errorf("btree: internal node key count %d exceeds uint16 range", len(n.Keys))
	}

	needed := internalEncodedSize(n)
	if needed > NodeSize {
		return nil, fmt.Errorf("btree: encoded internal node size %d exceeds NodeSize %d (too many/too-large keys for one node)", needed, NodeSize)
	}

	buf := make([]byte, NodeSize)
	buf[offNodeType] = nodeTypeInternal
	binary.LittleEndian.PutUint16(buf[offKeyCount:], uint16(len(n.Keys)))
	binary.LittleEndian.PutUint64(buf[offVersion:], n.Version)

	off := offBody
	off = encodeKeys(buf, off, n.Keys)
	for _, child := range n.Children {
		binary.LittleEndian.PutUint64(buf[off:], child)
		off += 8
	}
	binary.LittleEndian.PutUint64(buf[off:], n.NextSibling)
	off += 8
	off = encodeKeys(buf, off, []string{n.LowKey})

	return buf, nil
}

// encodeKeys writes each key in keys as a uint16 length prefix followed by its raw
// bytes, starting at off, and returns the offset immediately after the last key written.
// Callers must have already verified the destination buffer is large enough.
func encodeKeys(buf []byte, off int, keys []string) int {
	for _, k := range keys {
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(k)))
		off += 2
		copy(buf[off:], k)
		off += len(k)
	}
	return off
}

// decodeHeader reads the shared node header (type discriminator + key count + version
// counter) from data and returns them, or an error if data is too short or declares an
// unrecognized node type.
func decodeHeader(data []byte) (nodeType byte, keyCount int, version uint64, err error) {
	if len(data) < offBody {
		return 0, 0, 0, fmt.Errorf("btree: node buffer too short: got %d bytes, want at least %d", len(data), offBody)
	}
	nodeType = data[offNodeType]
	if nodeType != nodeTypeLeaf && nodeType != nodeTypeInternal {
		return 0, 0, 0, fmt.Errorf("btree: unrecognized node type discriminator: %d", nodeType)
	}
	keyCount = int(binary.LittleEndian.Uint16(data[offKeyCount:]))
	version = binary.LittleEndian.Uint64(data[offVersion:])
	return nodeType, keyCount, version, nil
}

// decodeKeys reads keyCount length-prefixed keys from data starting at off, and returns
// the decoded keys plus the offset immediately after the last key read. It returns an
// error if data is too short to hold the declared keys.
func decodeKeys(data []byte, off int, keyCount int) ([]string, int, error) {
	keys := make([]string, keyCount)
	for i := 0; i < keyCount; i++ {
		if off+2 > len(data) {
			return nil, 0, fmt.Errorf("btree: node buffer too short to read key %d length prefix", i)
		}
		klen := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		if off+klen > len(data) {
			return nil, 0, fmt.Errorf("btree: node buffer too short to read key %d (length %d)", i, klen)
		}
		keys[i] = string(data[off : off+klen])
		off += klen
	}
	return keys, off, nil
}

// DecodeLeafNode deserializes a NodeSize-byte buffer (as produced by LeafNode.Encode)
// back into a LeafNode. It returns an error if the buffer is too short, declares the
// wrong node type, or is too short to hold its declared key count / fileIDs / sibling
// pointer.
func DecodeLeafNode(data []byte) (LeafNode, error) {
	nodeType, keyCount, version, err := decodeHeader(data)
	if err != nil {
		return LeafNode{}, err
	}
	if nodeType != nodeTypeLeaf {
		return LeafNode{}, fmt.Errorf("btree: DecodeLeafNode called on non-leaf node (type %d)", nodeType)
	}

	keys, off, err := decodeKeys(data, offBody, keyCount)
	if err != nil {
		return LeafNode{}, err
	}

	var fileIDs []uint64
	if keyCount > 0 {
		fileIDs = make([]uint64, keyCount)
		for i := 0; i < keyCount; i++ {
			if off+8 > len(data) {
				return LeafNode{}, fmt.Errorf("btree: node buffer too short to read fileID %d", i)
			}
			fileIDs[i] = binary.LittleEndian.Uint64(data[off:])
			off += 8
		}
	}

	if off+8 > len(data) {
		return LeafNode{}, fmt.Errorf("btree: node buffer too short to read NextLeaf pointer")
	}
	nextLeaf := binary.LittleEndian.Uint64(data[off:])

	return LeafNode{Keys: keys, FileIDs: fileIDs, NextLeaf: nextLeaf, Version: version}, nil
}

// DecodeInternalNode deserializes a NodeSize-byte buffer (as produced by
// InternalNode.Encode) back into an InternalNode. It returns an error if the buffer is
// too short, declares the wrong node type, or is too short to hold its declared key
// count / children.
func DecodeInternalNode(data []byte) (InternalNode, error) {
	nodeType, keyCount, version, err := decodeHeader(data)
	if err != nil {
		return InternalNode{}, err
	}
	if nodeType != nodeTypeInternal {
		return InternalNode{}, fmt.Errorf("btree: DecodeInternalNode called on non-internal node (type %d)", nodeType)
	}

	keys, off, err := decodeKeys(data, offBody, keyCount)
	if err != nil {
		return InternalNode{}, err
	}

	childCount := keyCount + 1
	children := make([]uint64, childCount)
	for i := 0; i < childCount; i++ {
		if off+8 > len(data) {
			return InternalNode{}, fmt.Errorf("btree: node buffer too short to read child %d", i)
		}
		children[i] = binary.LittleEndian.Uint64(data[off:])
		off += 8
	}

	if off+8 > len(data) {
		return InternalNode{}, fmt.Errorf("btree: node buffer too short to read NextSibling pointer")
	}
	nextSibling := binary.LittleEndian.Uint64(data[off:])
	off += 8

	lowKeys, off, err := decodeKeys(data, off, 1)
	if err != nil {
		return InternalNode{}, fmt.Errorf("btree: node buffer too short to read LowKey: %w", err)
	}
	_ = off

	return InternalNode{Keys: keys, Children: children, Version: version, NextSibling: nextSibling, LowKey: lowKeys[0]}, nil
}

// OpenIndexFile opens the B+Tree index file at path, creating it (and any necessary
// permission bits, but not parent directories) if it does not already exist. This
// satisfies the "index/name.idx file is created on first use" acceptance criterion for
// subtask 1.2.1; wiring this into a full paged store (analogous to
// engine/catalog's FileManager) is left to later subtasks, since it is not required by
// this subtask's test spec.
func OpenIndexFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("btree: failed to open index file %q: %w", path, err)
	}
	return file, nil
}
