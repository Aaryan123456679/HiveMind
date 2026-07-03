package btree

import (
	"encoding/binary"
	"fmt"
	"os"
)

// rootStateSuffix names the small sidecar file, kept alongside the main index
// file, that durably persists the tree's current root node ID. This mirrors
// insert.go's NodeAllocator / nodeAllocSuffix (".nodealloc") pattern exactly:
// same directory, same fixed-size single-uint64-little-endian encoding, same
// WriteAt + Sync durability idiom. See docs/LLD/btree.md.
//
// This closes the gap explicitly called out in NodeAllocator's doc comment
// (insert.go): NodeAllocator durably persists its own high-water-mark across
// reopen of the same index file, but until this subtask nothing persisted
// "the current root node ID" -- callers had to track the newRootNodeID
// returned by Insert/Delete themselves, in memory only.
const rootStateSuffix = ".root"

// rootStateSize is the fixed size, in bytes, of the root sidecar file's
// contents: a single little-endian uint64 root node ID.
const rootStateSize = 8

// rootStatePath derives the sidecar path from store's underlying index file
// path, exactly the way NewNodeAllocator derives nodeAllocSuffix's path from
// store.f.Name().
func rootStatePath(store *NodeStore) string {
	return store.f.Name() + rootStateSuffix
}

// SaveRoot durably persists rootNodeID as the current root of the tree backed
// by store, so a later process (after this one exits, or after store's
// underlying file is closed and reopened) can recover it via LoadRoot.
//
// Design note (see docs/LLD/btree.md and this run's architecture-discovery.md
// for the full rationale): SaveRoot is deliberately NOT called from inside
// Insert or Delete. Both already return the possibly-new root node ID to
// their caller on every call; wiring persistence into them would force an
// fsync on every single insert/delete -- a significant hot-path cost with no
// acceptance criterion asking for it. Instead, callers own deciding *when* a
// newRootNodeID should be durably committed (e.g. once per batch, at a
// checkpoint boundary, or on clean shutdown) and call SaveRoot explicitly at
// that point. This keeps the hot insert/delete path free of a footgun where
// every operation forces a sync.
//
// Unlike NodeAllocator (which holds a long-lived sidecar file handle across
// many Next() calls for performance), SaveRoot opens, writes, syncs, and
// closes its sidecar file handle on every call: it is expected to be called
// comparatively rarely relative to individual Insert/Delete calls, so the
// simplicity of a stateless open/write/sync/close cycle outweighs the cost of
// not keeping a handle open.
func SaveRoot(store *NodeStore, rootNodeID uint64) error {
	path := rootStatePath(store)

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("btree: rootstate: open %s: %w", path, err)
	}
	defer f.Close()

	var buf [rootStateSize]byte
	binary.LittleEndian.PutUint64(buf[:], rootNodeID)
	if _, err := f.WriteAt(buf[:], 0); err != nil {
		return fmt.Errorf("btree: rootstate: persisting root node ID %d to %s: %w", rootNodeID, path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("btree: rootstate: syncing root node ID %d to %s: %w", rootNodeID, path, err)
	}
	return nil
}

// LoadRoot recovers the durably-persisted root node ID for the tree backed by
// store, as last written by SaveRoot for the same underlying index file.
//
// If the sidecar file does not exist yet -- i.e. SaveRoot has never been
// called for this index file (a fresh/new tree, or an existing tree whose
// caller has simply never chosen to persist a root) -- LoadRoot returns
// reservedNodeID (0) and a nil error, consistent with Insert's existing
// empty-tree bootstrap convention (rootNodeID == reservedNodeID means "no
// root exists yet"). This is a normal, expected outcome, not an error.
func LoadRoot(store *NodeStore) (rootNodeID uint64, err error) {
	path := rootStatePath(store)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return reservedNodeID, nil
		}
		return 0, fmt.Errorf("btree: rootstate: open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("btree: rootstate: stat %s: %w", path, err)
	}
	if info.Size() != rootStateSize {
		return 0, fmt.Errorf("btree: rootstate: state file %s has unexpected size %d (want %d)", path, info.Size(), rootStateSize)
	}

	var buf [rootStateSize]byte
	if _, err := f.ReadAt(buf[:], 0); err != nil {
		return 0, fmt.Errorf("btree: rootstate: reading root node ID from %s: %w", path, err)
	}

	return binary.LittleEndian.Uint64(buf[:]), nil
}
