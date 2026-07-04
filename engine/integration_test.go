// Package engine_test contains cross-package integration tests that wire
// together engine/catalog, engine/btree, and engine/wal the way a real
// caller would, rather than exercising each package in isolation (as their
// own unit tests already do extensively). It lives at the engine/ module
// root -- rather than inside any single subpackage -- specifically so it can
// import catalog, btree, and wal side by side as an external consumer would,
// with no special-cased access to any package's internals.
package engine_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// TestStorageCoreIntegration is subtask 1.5.1's required test: a single,
// single-threaded workload that wires catalog + btree + wal + content
// together to create several topic files, append to some of them, look them
// up by path (both via B+Tree point Lookup and via PrefixScan), and read
// their content back -- asserting that all four modules agree with each
// other at every step.
//
// Composition mirrors AGENT.md's storage-core design: the B+Tree maps
// topic-path -> fileID; the Catalog maps fileID -> metadata (including
// SizeBytes); ContentStore maps fileID -> actual .md bytes on disk. A
// fileID is the join key across all three. Every mutation (catalog Put via
// ContentStore.Create/Append, and the B+Tree Insert here) is WAL-logged
// before being applied in memory/on disk, per docs/LLD/wal.md's
// WAL-before-apply invariant -- ContentStore already does this internally
// for catalog mutations; this test does the equivalent explicitly for the
// B+Tree insert, since no production "storage engine facade" package exists
// yet to do it for the caller (that facade is out of this subtask's scope:
// 1.5.1 is integration-test-only, no new production code).
func TestStorageCoreIntegration(t *testing.T) {
	root := t.TempDir()

	// --- Open all four modules against one shared root, single-threaded. ---
	fm, err := catalog.Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		t.Fatalf("catalog.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := fm.Close(); err != nil {
			t.Errorf("FileManager.Close: %v", err)
		}
	})

	cat := catalog.NewCatalog(fm)

	idAlloc, err := catalog.NewIDAllocator(fm)
	if err != nil {
		t.Fatalf("catalog.NewIDAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := idAlloc.Close(); err != nil {
			t.Errorf("IDAllocator.Close: %v", err)
		}
	})

	walDir := filepath.Join(root, "wal")
	w, err := wal.OpenWriter(walDir, 1<<20)
	if err != nil {
		t.Fatalf("wal.OpenWriter: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("wal.Writer.Close: %v", err)
		}
	})

	cs, err := catalog.OpenContentStore(root, cat, w)
	if err != nil {
		t.Fatalf("catalog.OpenContentStore: %v", err)
	}

	indexPath := filepath.Join(root, "topics.idx")
	idxFile, err := btree.OpenIndexFile(indexPath)
	if err != nil {
		t.Fatalf("btree.OpenIndexFile: %v", err)
	}
	t.Cleanup(func() {
		if err := idxFile.Close(); err != nil {
			t.Errorf("index file Close: %v", err)
		}
	})

	store := btree.NewNodeStore(idxFile)

	nodeAlloc, err := btree.NewNodeAllocator(store)
	if err != nil {
		t.Fatalf("btree.NewNodeAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := nodeAlloc.Close(); err != nil {
			t.Errorf("NodeAllocator.Close: %v", err)
		}
	})

	var rootNodeID uint64 // reservedNodeID (0): empty tree, bootstrapped by first Insert.

	// insertPath WAL-logs and then applies a B+Tree insert for path->fileID,
	// mirroring the WAL-before-apply pattern ContentStore already uses
	// internally for catalog mutations (docs/LLD/wal.md's invariant: "every
	// mutation to the catalog or any index must be logged in the WAL before
	// it is applied in memory or on disk").
	insertPath := func(path string, fileID uint64) {
		t.Helper()
		rec := wal.NewBTreeInsertRecord(path, fileID)
		if _, err := wal.AppendAndApply(w, rec, func() error {
			newRootNodeID, err := btree.Insert(store, nodeAlloc, rootNodeID, path, fileID)
			if err != nil {
				return err
			}
			rootNodeID = newRootNodeID
			return nil
		}); err != nil {
			t.Fatalf("insertPath(%q): %v", path, err)
		}
	}

	// --- Step 1: create several topic files under two topic prefixes. ---
	const filesPerPrefix = 4
	prefixes := []string{"topics/alpha/", "topics/beta/"}

	type fileState struct {
		path    string
		fileID  uint64
		content []byte
	}
	// Pre-allocate capacity so appends below never reallocate the backing
	// array: byPath stores pointers into this slice, which would otherwise
	// be silently invalidated by a reallocating append.
	files := make([]fileState, 0, len(prefixes)*filesPerPrefix)
	byPath := make(map[string]*fileState)

	for _, prefix := range prefixes {
		for i := 0; i < filesPerPrefix; i++ {
			path := fmt.Sprintf("%sfile%02d", prefix, i)

			fileID, err := idAlloc.Next()
			if err != nil {
				t.Fatalf("IDAllocator.Next for %q: %v", path, err)
			}

			content := []byte(fmt.Sprintf("# %s\n\ninitial content for %s\n", path, path))

			rec := catalog.CatalogRecord{
				FileID:         fileID,
				PathHash:       fileID * 31,
				CurrentVersion: 1,
				SizeBytes:      uint64(len(content)),
				Status:         catalog.StatusActive,
			}
			if _, err := cs.Create(rec, content); err != nil {
				t.Fatalf("ContentStore.Create for %q (fileID %d): %v", path, fileID, err)
			}

			insertPath(path, fileID)

			fs := fileState{path: path, fileID: fileID, content: content}
			files = append(files, fs)
			byPath[path] = &files[len(files)-1]
		}
	}

	// --- Step 2: append to a subset of the created files. ---
	// Ordinary append: a small suffix to the first file under each prefix.
	for _, prefix := range prefixes {
		path := prefix + "file00"
		fs := byPath[path]
		suffix := []byte("\nappended line.\n")
		crossed, err := cs.Append(fs.fileID, suffix)
		if err != nil {
			t.Fatalf("ContentStore.Append for %q: %v", path, err)
		}
		if crossed {
			t.Fatalf("ContentStore.Append for %q: unexpected threshold crossing on small append", path)
		}
		fs.content = append(append([]byte(nil), fs.content...), suffix...)
	}

	// Threshold-crossing append: push one file's content from well under
	// 8KiB (defaultSplitThresholdBytes) to well over it in a single Append
	// call, asserting the documented "fires exactly once, on the crossing
	// call" signal end-to-end (not just in content_test.go's unit test).
	thresholdPath := "topics/alpha/file01"
	thresholdFS := byPath[thresholdPath]
	bigChunk := bytes.Repeat([]byte("x"), 9*1024)
	crossed, err := cs.Append(thresholdFS.fileID, bigChunk)
	if err != nil {
		t.Fatalf("ContentStore.Append (threshold) for %q: %v", thresholdPath, err)
	}
	if !crossed {
		t.Fatalf("ContentStore.Append (threshold) for %q: want thresholdCrossed=true, got false", thresholdPath)
	}
	thresholdFS.content = append(append([]byte(nil), thresholdFS.content...), bigChunk...)

	// A second append to the SAME file, now already over threshold, must
	// NOT report crossing again (fires exactly once).
	crossedAgain, err := cs.Append(thresholdFS.fileID, []byte("more\n"))
	if err != nil {
		t.Fatalf("ContentStore.Append (post-threshold) for %q: %v", thresholdPath, err)
	}
	if crossedAgain {
		t.Fatalf("ContentStore.Append (post-threshold) for %q: threshold signal fired a second time", thresholdPath)
	}
	thresholdFS.content = append(append([]byte(nil), thresholdFS.content...), []byte("more\n")...)

	// --- Step 3: PrefixScan each topic prefix and cross-check against the
	// catalog + content store for every resolved fileID. ---
	for _, prefix := range prefixes {
		scanPrefix := prefix // scan.go's PrefixScan takes a literal string prefix, not a glob.
		entries, err := btree.PrefixScan(store, rootNodeID, scanPrefix)
		if err != nil {
			t.Fatalf("PrefixScan(%q): %v", scanPrefix, err)
		}
		if len(entries) != filesPerPrefix {
			t.Fatalf("PrefixScan(%q): got %d entries, want %d", scanPrefix, len(entries), filesPerPrefix)
		}
		if !sort.SliceIsSorted(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path }) {
			t.Fatalf("PrefixScan(%q): entries not sorted: %+v", scanPrefix, entries)
		}

		for _, entry := range entries {
			fs, ok := byPath[entry.Path]
			if !ok {
				t.Fatalf("PrefixScan(%q): unexpected path %q not among inserted files", scanPrefix, entry.Path)
			}
			if entry.FileID != fs.fileID {
				t.Fatalf("PrefixScan(%q): path %q resolved to fileID %d, want %d", scanPrefix, entry.Path, entry.FileID, fs.fileID)
			}

			// Cross-check catalog: SizeBytes must match the actual final
			// content length (post-append).
			rec, err := cat.Get(entry.FileID)
			if err != nil {
				t.Fatalf("catalog.Get(%d) for path %q: %v", entry.FileID, entry.Path, err)
			}
			if rec.SizeBytes != uint64(len(fs.content)) {
				t.Fatalf("catalog.Get(%d) for path %q: SizeBytes = %d, want %d", entry.FileID, entry.Path, rec.SizeBytes, len(fs.content))
			}

			// Cross-check content store: full read-back must be byte-for-byte
			// identical to what was written/appended.
			data, err := cs.Read(entry.FileID)
			if err != nil {
				t.Fatalf("ContentStore.Read(%d) for path %q: %v", entry.FileID, entry.Path, err)
			}
			if !bytes.Equal(data, fs.content) {
				t.Fatalf("ContentStore.Read(%d) for path %q: content mismatch:\ngot:  %q\nwant: %q", entry.FileID, entry.Path, data, fs.content)
			}
		}
	}

	// --- Step 4: independent point Lookup for every inserted path, cross-
	// checked against PrefixScan's results. ---
	for _, fs := range files {
		fileID, found, err := btree.Lookup(store, rootNodeID, fs.path)
		if err != nil {
			t.Fatalf("btree.Lookup(%q): %v", fs.path, err)
		}
		if !found {
			t.Fatalf("btree.Lookup(%q): not found, want fileID %d", fs.path, fs.fileID)
		}
		if fileID != fs.fileID {
			t.Fatalf("btree.Lookup(%q) = %d, want %d", fs.path, fileID, fs.fileID)
		}

		data, err := cs.Read(fileID)
		if err != nil {
			t.Fatalf("ContentStore.Read(%d) via Lookup(%q): %v", fileID, fs.path, err)
		}
		if !bytes.Equal(data, fs.content) {
			t.Fatalf("ContentStore.Read(%d) via Lookup(%q): content mismatch:\ngot:  %q\nwant: %q", fileID, fs.path, data, fs.content)
		}
	}

	// --- Step 5: negative cases -- absent prefix/path must not be found. ---
	noEntries, err := btree.PrefixScan(store, rootNodeID, "topics/nonexistent/")
	if err != nil {
		t.Fatalf("PrefixScan(nonexistent prefix): %v", err)
	}
	if len(noEntries) != 0 {
		t.Fatalf("PrefixScan(nonexistent prefix): got %d entries, want 0", len(noEntries))
	}

	if _, found, err := btree.Lookup(store, rootNodeID, "topics/alpha/does-not-exist"); err != nil {
		t.Fatalf("btree.Lookup(nonexistent path): %v", err)
	} else if found {
		t.Fatalf("btree.Lookup(nonexistent path): found=true, want false")
	}

	if _, err := cat.Get(999_999); err == nil {
		t.Fatalf("catalog.Get(never-allocated fileID): want error, got nil")
	}
}
