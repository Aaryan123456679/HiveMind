package btree

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// TestPersistReload is this subtask's (1.2.6) required test spec: build a
// tree, save its root, close the underlying index file, reopen the SAME
// on-disk file fresh (simulating a process restart -- not reusing the
// in-memory tree), recover the root node ID via LoadRoot, and assert that
// Lookup and PrefixScan return identical results to before closing.
func TestPersistReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "name.idx")

	// --- "before restart": build the tree, save the root, then close. ---
	f, err := OpenIndexFile(path)
	if err != nil {
		t.Fatalf("OpenIndexFile: %v", err)
	}
	store := NewNodeStore(f)
	alloc, err := NewNodeAllocator(store)
	if err != nil {
		t.Fatalf("NewNodeAllocator: %v", err)
	}

	// Enough keys to force multiple levels of leaf/internal splitting (real
	// multi-leaf structure), reusing insert_test.go's genKey/insertN helpers
	// (same package) -- the identical pattern used by 1.2.3/1.2.4/1.2.5's
	// tests.
	const n = 400
	rootID, inserted := insertN(t, store, alloc, n)

	if err := SaveRoot(store, rootID); err != nil {
		t.Fatalf("SaveRoot: %v", err)
	}

	// Record expected results from the pre-close tree before tearing it down.
	wantLookups := make(map[string]uint64, len(inserted))
	for k, v := range inserted {
		wantLookups[k] = v
	}

	prefixes := []string{"topic0", "topic00", "topic01", "topic1", "nonexistent"}
	wantScans := make(map[string][]ScanEntry, len(prefixes))
	for _, p := range prefixes {
		got, err := PrefixScan(store, rootID, p)
		if err != nil {
			t.Fatalf("PrefixScan(%q) before close: unexpected error: %v", p, err)
		}
		wantScans[p] = got
	}

	if err := alloc.Close(); err != nil {
		t.Fatalf("closing allocator sidecar file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing index file: %v", err)
	}

	// --- "after restart": reopen the same on-disk file completely fresh. ---
	f2, err := OpenIndexFile(path)
	if err != nil {
		t.Fatalf("OpenIndexFile (reopen): %v", err)
	}
	t.Cleanup(func() { f2.Close() })
	store2 := NewNodeStore(f2)

	recoveredRootID, err := LoadRoot(store2)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	if recoveredRootID != rootID {
		t.Fatalf("LoadRoot recovered root ID %d, want %d", recoveredRootID, rootID)
	}

	// Lookup parity: every originally-inserted key must resolve to the same
	// fileID after reopen.
	for k, wantFileID := range wantLookups {
		gotFileID, found, err := Lookup(store2, recoveredRootID, k)
		if err != nil {
			t.Fatalf("Lookup(%q) after reopen: unexpected error: %v", k, err)
		}
		if !found {
			t.Fatalf("Lookup(%q) after reopen: not found, want fileID %d", k, wantFileID)
		}
		if gotFileID != wantFileID {
			t.Fatalf("Lookup(%q) after reopen = %d, want %d", k, gotFileID, wantFileID)
		}
	}

	// PrefixScan parity: identical results, in identical order, for the same
	// set of prefixes queried before closing.
	for _, p := range prefixes {
		got, err := PrefixScan(store2, recoveredRootID, p)
		if err != nil {
			t.Fatalf("PrefixScan(%q) after reopen: unexpected error: %v", p, err)
		}
		want := wantScans[p]
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("PrefixScan(%q) after reopen = %+v, want %+v", p, got, want)
		}
		if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Path < got[j].Path }) {
			t.Fatalf("PrefixScan(%q) after reopen is not sorted: %+v", p, got)
		}
	}
}

// TestLoadRootFreshIndexFile covers the "sidecar doesn't exist yet" case: a
// fresh index file that has never had SaveRoot called against it must yield
// reservedNodeID (0) from LoadRoot with no error -- not a crash -- consistent
// with Insert's empty-tree bootstrap convention.
func TestLoadRootFreshIndexFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "name.idx")

	f, err := OpenIndexFile(path)
	if err != nil {
		t.Fatalf("OpenIndexFile: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	store := NewNodeStore(f)

	rootID, err := LoadRoot(store)
	if err != nil {
		t.Fatalf("LoadRoot on fresh index file: unexpected error: %v", err)
	}
	if rootID != reservedNodeID {
		t.Fatalf("LoadRoot on fresh index file = %d, want reservedNodeID (%d)", rootID, reservedNodeID)
	}
}
