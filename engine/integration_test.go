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
	"encoding/binary"
	"errors"
	"fmt"
	"os"
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

// TestStorageCoreCrashRecovery is subtask 1.5.2's required test: it extends
// TestStorageCoreIntegration's single-process wiring with a second
// generation that simulates a process crash mid-append and a subsequent
// restart, asserting the four modules (catalog, btree, content, wal) come
// back into a mutually consistent state via WAL recovery, with no
// partial/corrupted file left visible for the interrupted mutation.
//
// Architecture note (see this run's architecture-discovery.md): btree is NOT
// rebuilt from the recovered Catalog here, because Catalog's on-disk record
// (see catalog/record.go) only stores a PathHash, never the literal topic
// path -- there is no API by which a recovered Catalog could answer "what
// path did fileID N have". btree instead has its own self-contained,
// WAL-independent on-disk persistence/recovery story (btree/persist.go):
// NodeStore.WriteNode durably writes every node's bytes into the index file
// at Insert/Delete time, and SaveRoot/LoadRoot durably persist/recover the
// tree's current root node ID across a restart. This test checkpoints the
// root via SaveRoot once, after every mutation it wants to survive the
// simulated crash, then recovers it via LoadRoot after "restarting" --
// exactly the caller-decides-when-to-checkpoint contract persist.go
// documents.
func TestStorageCoreCrashRecovery(t *testing.T) {
	root := t.TempDir()
	walDir := filepath.Join(root, "wal")
	indexPath := filepath.Join(root, "topics.idx")
	const maxSegmentBytes = 1 << 20

	// ================= Generation 1: pre-crash ================= //

	fm1, err := catalog.Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		t.Fatalf("catalog.Open (gen1): %v", err)
	}
	cat1 := catalog.NewCatalog(fm1)

	idAlloc1, err := catalog.NewIDAllocator(fm1)
	if err != nil {
		t.Fatalf("catalog.NewIDAllocator (gen1): %v", err)
	}

	w1, err := wal.OpenWriter(walDir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("wal.OpenWriter (gen1): %v", err)
	}

	cs1, err := catalog.OpenContentStore(root, cat1, w1)
	if err != nil {
		t.Fatalf("catalog.OpenContentStore (gen1): %v", err)
	}

	idxFile1, err := btree.OpenIndexFile(indexPath)
	if err != nil {
		t.Fatalf("btree.OpenIndexFile (gen1): %v", err)
	}
	store1 := btree.NewNodeStore(idxFile1)

	nodeAlloc1, err := btree.NewNodeAllocator(store1)
	if err != nil {
		t.Fatalf("btree.NewNodeAllocator (gen1): %v", err)
	}

	var rootNodeID uint64 // reservedNodeID (0): empty tree.

	insertPath := func(path string, fileID uint64) error {
		rec := wal.NewBTreeInsertRecord(path, fileID)
		_, err := wal.AppendAndApply(w1, rec, func() error {
			newRootNodeID, err := btree.Insert(store1, nodeAlloc1, rootNodeID, path, fileID)
			if err != nil {
				return err
			}
			rootNodeID = newRootNodeID
			return nil
		})
		return err
	}

	type fileState struct {
		path    string
		fileID  uint64
		content []byte
	}

	// --- Step 1: a few fully-committed topic files. --- //
	const numCommitted = 3
	committed := make([]fileState, 0, numCommitted)
	for i := 0; i < numCommitted; i++ {
		path := fmt.Sprintf("topics/keep/file%02d", i)

		fileID, err := idAlloc1.Next()
		if err != nil {
			t.Fatalf("IDAllocator.Next %q: %v", path, err)
		}

		content := []byte(fmt.Sprintf("# %s\n\ninitial content %s\n", path, path))
		rec := catalog.CatalogRecord{
			FileID:         fileID,
			PathHash:       fileID * 31,
			CurrentVersion: 1,
			SizeBytes:      uint64(len(content)),
			Status:         catalog.StatusActive,
		}
		if _, err := cs1.Create(rec, content); err != nil {
			t.Fatalf("ContentStore.Create %q (fileID %d): %v", path, fileID, err)
		}
		if err := insertPath(path, fileID); err != nil {
			t.Fatalf("insertPath(%q): %v", path, err)
		}

		committed = append(committed, fileState{path: path, fileID: fileID, content: content})
	}

	// --- Step 2: one fully-committed Append, through the normal
	// WAL-before-apply path (must survive the simulated crash below). --- //
	appendedFS := &committed[0]
	suffix := []byte("\nappended before the crash.\n")
	if _, err := cs1.Append(appendedFS.fileID, suffix); err != nil {
		t.Fatalf("ContentStore.Append %q: %v", appendedFS.path, err)
	}
	appendedFS.content = append(append([]byte(nil), appendedFS.content...), suffix...)

	// --- Step 3: checkpoint the btree's root, reflecting every insert
	// above -- the point a real caller would persist state before an
	// orderly shutdown, or periodically during normal operation. --- //
	if err := btree.SaveRoot(store1, rootNodeID); err != nil {
		t.Fatalf("btree.SaveRoot: %v", err)
	}

	// --- Step 4: simulate a crash mid-append. Allocate a fileID for a
	// file that is never actually created: this models a
	// ContentStore.Create/Append call whose WAL record was still being
	// written when the process died, so (per the WAL-before-apply
	// invariant) its apply step -- content file write, catalog Put,
	// btree Insert -- never ran. Nothing about this fileID should be
	// observable anywhere after recovery. --- //
	crashedPath := "topics/keep/crashed"
	crashedFileID, err := idAlloc1.Next()
	if err != nil {
		t.Fatalf("IDAllocator.Next (crashed file): %v", err)
	}
	crashedContentPath := cs1.ContentPath(crashedFileID)

	segNum := w1.SegmentNum()

	if err := idAlloc1.Close(); err != nil {
		t.Fatalf("IDAllocator.Close (gen1): %v", err)
	}
	if err := nodeAlloc1.Close(); err != nil {
		t.Fatalf("NodeAllocator.Close (gen1): %v", err)
	}
	if err := idxFile1.Close(); err != nil {
		t.Fatalf("index file Close (gen1): %v", err)
	}
	if err := w1.Close(); err != nil {
		t.Fatalf("wal.Writer.Close (gen1): %v", err)
	}
	if err := fm1.Close(); err != nil {
		t.Fatalf("FileManager.Close (gen1): %v", err)
	}

	// Inject a torn record directly onto the tail of the active WAL
	// segment: a full 8-byte header (4-byte little-endian payload
	// length, 4-byte little-endian CRC) claiming a large payload, but
	// with only a handful of payload bytes actually on disk -- exactly
	// the "torn payload at tail" crash-injection recipe
	// engine/wal/recovery_test.go's TestCrashInjectionRecovery (1.3.5)
	// established. Both wal.Replay and wal.OpenWriter treat this as a
	// torn tail (crash-tolerant, silently discarded), not a hard error.
	segPath := filepath.Join(walDir, fmt.Sprintf("wal-%d.log", segNum))
	tornFile, err := os.OpenFile(segPath, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("opening WAL segment %s to inject torn record: %v", segPath, err)
	}
	var header [8]byte
	binary.LittleEndian.PutUint32(header[0:4], 500) // claimed payload length
	binary.LittleEndian.PutUint32(header[4:8], 0xCAFEBABE)
	if _, err := tornFile.Write(header[:]); err != nil {
		t.Fatalf("writing torn header: %v", err)
	}
	if _, err := tornFile.Write([]byte("partial-payload-bytes-only")); err != nil {
		t.Fatalf("writing torn payload: %v", err)
	}
	if err := tornFile.Close(); err != nil {
		t.Fatalf("closing torn WAL segment: %v", err)
	}

	// ================= Generation 2: restart ================= //

	fm2, err := catalog.Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		t.Fatalf("catalog.Open (gen2): %v", err)
	}
	t.Cleanup(func() {
		if err := fm2.Close(); err != nil {
			t.Errorf("FileManager.Close (gen2): %v", err)
		}
	})
	cat2 := catalog.NewCatalog(fm2)

	idAlloc2, err := catalog.NewIDAllocator(fm2)
	if err != nil {
		t.Fatalf("catalog.NewIDAllocator (gen2): %v", err)
	}
	t.Cleanup(func() {
		if err := idAlloc2.Close(); err != nil {
			t.Errorf("IDAllocator.Close (gen2): %v", err)
		}
	})

	// wal.OpenWriter itself discards the torn tail on resume, so the
	// directory is immediately safe to append to again.
	w2, err := wal.OpenWriter(walDir, maxSegmentBytes)
	if err != nil {
		t.Fatalf("wal.OpenWriter (gen2): %v", err)
	}
	t.Cleanup(func() {
		if err := w2.Close(); err != nil {
			t.Errorf("wal.Writer.Close (gen2): %v", err)
		}
	})

	if err := catalog.RecoverFromWAL(cat2, walDir); err != nil {
		t.Fatalf("catalog.RecoverFromWAL: %v", err)
	}

	cs2, err := catalog.OpenContentStore(root, cat2, w2)
	if err != nil {
		t.Fatalf("catalog.OpenContentStore (gen2): %v", err)
	}

	idxFile2, err := btree.OpenIndexFile(indexPath)
	if err != nil {
		t.Fatalf("btree.OpenIndexFile (gen2): %v", err)
	}
	t.Cleanup(func() {
		if err := idxFile2.Close(); err != nil {
			t.Errorf("index file Close (gen2): %v", err)
		}
	})
	store2 := btree.NewNodeStore(idxFile2)

	nodeAlloc2, err := btree.NewNodeAllocator(store2)
	if err != nil {
		t.Fatalf("btree.NewNodeAllocator (gen2): %v", err)
	}
	t.Cleanup(func() {
		if err := nodeAlloc2.Close(); err != nil {
			t.Errorf("NodeAllocator.Close (gen2): %v", err)
		}
	})

	rootNodeID2, err := btree.LoadRoot(store2)
	if err != nil {
		t.Fatalf("btree.LoadRoot: %v", err)
	}

	// ================= Assertions ================= //

	// Every fully-committed pre-crash file (including the appended one)
	// must be found and mutually consistent across btree -> catalog ->
	// content.
	for _, fs := range committed {
		fileID, found, err := btree.Lookup(store2, rootNodeID2, fs.path)
		if err != nil {
			t.Fatalf("btree.Lookup(%q) post-recovery: %v", fs.path, err)
		}
		if !found {
			t.Fatalf("btree.Lookup(%q) post-recovery: not found, want fileID %d", fs.path, fs.fileID)
		}
		if fileID != fs.fileID {
			t.Fatalf("btree.Lookup(%q) post-recovery = %d, want %d", fs.path, fileID, fs.fileID)
		}

		rec, err := cat2.Get(fileID)
		if err != nil {
			t.Fatalf("catalog.Get(%d) path %q post-recovery: %v", fileID, fs.path, err)
		}
		if rec.SizeBytes != uint64(len(fs.content)) {
			t.Fatalf("catalog.Get(%d) path %q post-recovery: SizeBytes = %d, want %d", fileID, fs.path, rec.SizeBytes, len(fs.content))
		}

		data, err := cs2.Read(fileID)
		if err != nil {
			t.Fatalf("ContentStore.Read(%d) path %q post-recovery: %v", fileID, fs.path, err)
		}
		if !bytes.Equal(data, fs.content) {
			t.Fatalf("ContentStore.Read(%d) path %q post-recovery: content mismatch:\ngot:  %q\nwant: %q", fileID, fs.path, data, fs.content)
		}
	}

	// PrefixScan must see exactly the committed files -- not the crashed
	// one -- under their shared prefix.
	entries, err := btree.PrefixScan(store2, rootNodeID2, "topics/keep/")
	if err != nil {
		t.Fatalf("PrefixScan(topics/keep/) post-recovery: %v", err)
	}
	if len(entries) != numCommitted {
		t.Fatalf("PrefixScan(topics/keep/) post-recovery: got %d entries, want %d (crashed entry must not be indexed)", len(entries), numCommitted)
	}

	// The interrupted mutation must be invisible everywhere: no btree
	// entry, no catalog record, and -- critically -- no content file on
	// disk at all (not even a partial/corrupted one).
	if _, found, err := btree.Lookup(store2, rootNodeID2, crashedPath); err != nil {
		t.Fatalf("btree.Lookup(%q) post-recovery: %v", crashedPath, err)
	} else if found {
		t.Fatalf("btree.Lookup(%q) post-recovery: found=true, want false (crashed insert must not have been applied)", crashedPath)
	}

	if _, err := cat2.Get(crashedFileID); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("catalog.Get(%d) (crashed fileID) post-recovery: got %v, want %v", crashedFileID, err, catalog.ErrNotFound)
	}

	if _, err := os.Stat(crashedContentPath); !os.IsNotExist(err) {
		t.Fatalf("os.Stat(%s) (crashed content file) post-recovery: got err=%v, want a not-exist error (no partial/corrupted content file may be visible)", crashedContentPath, err)
	}

	// The recovered system must remain fully usable, not just
	// read-consistent: a brand-new post-recovery write must succeed and
	// round-trip cleanly through all four modules.
	newPath := "topics/keep/post-recovery"
	newFileID, err := idAlloc2.Next()
	if err != nil {
		t.Fatalf("IDAllocator.Next (post-recovery): %v", err)
	}
	newContent := []byte("# post recovery\n\nwritten after restart.\n")
	newRec := catalog.CatalogRecord{
		FileID:         newFileID,
		PathHash:       newFileID * 31,
		CurrentVersion: 1,
		SizeBytes:      uint64(len(newContent)),
		Status:         catalog.StatusActive,
	}
	if _, err := cs2.Create(newRec, newContent); err != nil {
		t.Fatalf("ContentStore.Create (post-recovery) %q: %v", newPath, err)
	}
	newRootRec := wal.NewBTreeInsertRecord(newPath, newFileID)
	if _, err := wal.AppendAndApply(w2, newRootRec, func() error {
		updatedRoot, err := btree.Insert(store2, nodeAlloc2, rootNodeID2, newPath, newFileID)
		if err != nil {
			return err
		}
		rootNodeID2 = updatedRoot
		return nil
	}); err != nil {
		t.Fatalf("insert (post-recovery) %q: %v", newPath, err)
	}

	if fileID, found, err := btree.Lookup(store2, rootNodeID2, newPath); err != nil {
		t.Fatalf("btree.Lookup(%q) (post-recovery write): %v", newPath, err)
	} else if !found || fileID != newFileID {
		t.Fatalf("btree.Lookup(%q) (post-recovery write) = (%d, %v), want (%d, true)", newPath, fileID, found, newFileID)
	}
	if data, err := cs2.Read(newFileID); err != nil {
		t.Fatalf("ContentStore.Read (post-recovery write) %q: %v", newPath, err)
	} else if !bytes.Equal(data, newContent) {
		t.Fatalf("ContentStore.Read (post-recovery write) %q = %q, want %q", newPath, data, newContent)
	}
}
