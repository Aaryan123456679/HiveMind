package split

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/graph"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// newTestContentStoreDeps opens a fresh catalog.FileManager-backed
// catalog.IDAllocator and catalog.ContentStore, rooted at an isolated
// t.TempDir(), reusing the same newTestCatalog/newTestWAL helpers
// orchestrate_test.go already defines in this package.
func newTestContentStoreDeps(t *testing.T) (*catalog.IDAllocator, *catalog.ContentStore, *catalog.Catalog) {
	t.Helper()
	idAlloc, cs, cat, _ := newTestContentStoreDepsWithWAL(t)
	return idAlloc, cs, cat
}

// newTestContentStoreDepsWithWAL is newTestContentStoreDeps, additionally
// returning the underlying *wal.Writer -- needed by ExecuteSplitRedirectStub's
// tests (2b.3.2), which must durably transition the catalog record via the
// same wal.Writer the ContentStore itself uses.
func newTestContentStoreDepsWithWAL(t *testing.T) (*catalog.IDAllocator, *catalog.ContentStore, *catalog.Catalog, *wal.Writer) {
	t.Helper()

	root := t.TempDir()

	fm, err := catalog.Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		t.Fatalf("catalog.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := fm.Close(); err != nil {
			t.Errorf("FileManager.Close: %v", err)
		}
	})

	idAlloc, err := catalog.NewIDAllocator(fm)
	if err != nil {
		t.Fatalf("catalog.NewIDAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := idAlloc.Close(); err != nil {
			t.Errorf("IDAllocator.Close: %v", err)
		}
	})

	cat := catalog.NewCatalog(fm)
	w := newTestWAL(t, root)

	cs, err := catalog.OpenContentStore(root, cat, w)
	if err != nil {
		t.Fatalf("catalog.OpenContentStore: %v", err)
	}

	return idAlloc, cs, cat, w
}

func TestSplitAllocateAndWrite(t *testing.T) {
	t.Run("fixture_plan", func(t *testing.T) {
		idAlloc, cs, cat := newTestContentStoreDeps(t)

		result, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, FixtureFileContent, FixtureSplitPlan)
		if err != nil {
			t.Fatalf("ExecuteSplitAllocateAndWrite: %v", err)
		}

		if len(result) != len(FixtureSplitPlan.Files) {
			t.Fatalf("result has %d entries, want %d", len(result), len(FixtureSplitPlan.Files))
		}

		seenFileIDs := make(map[uint64]bool)
		for _, proposal := range FixtureSplitPlan.Files {
			fileID, ok := result[proposal.NewPath]
			if !ok {
				t.Fatalf("result missing entry for NewPath %q", proposal.NewPath)
			}
			if fileID == catalog.InvalidFileID {
				t.Fatalf("result for %q has InvalidFileID", proposal.NewPath)
			}
			if seenFileIDs[fileID] {
				t.Fatalf("fileID %d allocated more than once", fileID)
			}
			seenFileIDs[fileID] = true

			want := extractSections(FixtureFileContent, proposal.SectionRanges)

			got, err := os.ReadFile(cs.ContentPath(fileID))
			if err != nil {
				t.Fatalf("reading written content file for %q (fileID %d): %v", proposal.NewPath, fileID, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("content for %q (fileID %d) = %q, want %q", proposal.NewPath, fileID, got, want)
			}

			// This subtask must not create any catalog visibility for the new
			// fileID -- that is 2b.3.2's job. Confirm no CatalogRecord exists.
			if _, err := cat.Get(fileID); !errors.Is(err, catalog.ErrNotFound) {
				t.Errorf("cat.Get(%d) = %v, want wrapped ErrNotFound (no catalog mutation should happen in this subtask)", fileID, err)
			}
		}
	})

	t.Run("multi_range_single_file", func(t *testing.T) {
		idAlloc, cs, _ := newTestContentStoreDeps(t)

		content := []byte("ABCDEFGHIJKLMNOP")
		plan := SplitPlan{
			Files: []SplitFileProposal{
				{
					NewPath: "assembled.md",
					SectionRanges: []SectionRange{
						{Start: 10, End: 16}, // "KLMNOP"
						{Start: 0, End: 4},   // "ABCD"
					},
				},
			},
		}

		result, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, content, plan)
		if err != nil {
			t.Fatalf("ExecuteSplitAllocateAndWrite: %v", err)
		}

		fileID := result["assembled.md"]
		got, err := os.ReadFile(cs.ContentPath(fileID))
		if err != nil {
			t.Fatalf("reading written content file: %v", err)
		}
		want := "KLMNOPABCD"
		if string(got) != want {
			t.Errorf("content = %q, want %q", got, want)
		}
	})

	t.Run("empty_range", func(t *testing.T) {
		idAlloc, cs, _ := newTestContentStoreDeps(t)

		content := []byte("hello world")
		plan := SplitPlan{
			Files: []SplitFileProposal{
				{
					NewPath: "empty.md",
					SectionRanges: []SectionRange{
						{Start: 5, End: 5},
					},
				},
			},
		}

		result, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, content, plan)
		if err != nil {
			t.Fatalf("ExecuteSplitAllocateAndWrite: %v", err)
		}

		fileID := result["empty.md"]
		got, err := os.ReadFile(cs.ContentPath(fileID))
		if err != nil {
			t.Fatalf("reading written content file: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("content = %q, want empty", got)
		}
	})

	t.Run("out_of_bounds_range", func(t *testing.T) {
		idAlloc, cs, _ := newTestContentStoreDeps(t)

		content := []byte("short")
		plan := SplitPlan{
			Files: []SplitFileProposal{
				{
					NewPath:       "oob.md",
					SectionRanges: []SectionRange{{Start: 0, End: 100}},
				},
			},
		}

		if _, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, content, plan); err == nil {
			t.Fatal("expected error for out-of-bounds section range, got nil")
		}
	})

	t.Run("inverted_range", func(t *testing.T) {
		idAlloc, cs, _ := newTestContentStoreDeps(t)

		content := []byte("short")
		plan := SplitPlan{
			Files: []SplitFileProposal{
				{
					NewPath:       "inverted.md",
					SectionRanges: []SectionRange{{Start: 3, End: 1}},
				},
			},
		}

		if _, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, content, plan); err == nil {
			t.Fatal("expected error for inverted section range, got nil")
		}
	})

	t.Run("overlapping_ranges", func(t *testing.T) {
		idAlloc, cs, _ := newTestContentStoreDeps(t)

		content := []byte("0123456789")
		plan := SplitPlan{
			Files: []SplitFileProposal{
				{NewPath: "a.md", SectionRanges: []SectionRange{{Start: 0, End: 6}}},
				{NewPath: "b.md", SectionRanges: []SectionRange{{Start: 5, End: 10}}},
			},
		}

		if _, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, content, plan); err == nil {
			t.Fatal("expected error for overlapping section ranges, got nil")
		}
	})

	t.Run("duplicate_new_path", func(t *testing.T) {
		idAlloc, cs, _ := newTestContentStoreDeps(t)

		content := []byte("0123456789")
		plan := SplitPlan{
			Files: []SplitFileProposal{
				{NewPath: "dup.md", SectionRanges: []SectionRange{{Start: 0, End: 5}}},
				{NewPath: "dup.md", SectionRanges: []SectionRange{{Start: 5, End: 10}}},
			},
		}

		if _, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, content, plan); err == nil {
			t.Fatal("expected error for duplicate NewPath, got nil")
		}
	})

	t.Run("empty_plan", func(t *testing.T) {
		idAlloc, cs, _ := newTestContentStoreDeps(t)

		if _, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, []byte("content"), SplitPlan{}); err == nil {
			t.Fatal("expected error for empty split plan, got nil")
		}
	})

	t.Run("nil_deps", func(t *testing.T) {
		idAlloc, cs, _ := newTestContentStoreDeps(t)

		if _, err := ExecuteSplitAllocateAndWrite(nil, cs, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Fatal("expected error for nil idAlloc, got nil")
		}
		if _, err := ExecuteSplitAllocateAndWrite(idAlloc, nil, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Fatal("expected error for nil cs, got nil")
		}
	})
}

// putSplitRecord seeds a CatalogRecord for fileID directly via cat.Put with
// Status = catalog.StatusSplit, simulating the state Orchestrator.EndSplit(
// fileID, catalog.StatusSplit) (2b.1.3) would have already left behind
// before ExecuteSplitRedirectStub (2b.3.2) is ever called.
func putSplitRecord(t *testing.T, cat *catalog.Catalog, fileID uint64, sizeBytes uint64) {
	t.Helper()
	if err := cat.Put(catalog.CatalogRecord{
		FileID:         fileID,
		CurrentVersion: 0,
		SizeBytes:      sizeBytes,
		Status:         catalog.StatusSplit,
	}); err != nil {
		t.Fatalf("seeding StatusSplit record for fileID %d: %v", fileID, err)
	}
}

func TestSplitRedirectStub(t *testing.T) {
	t.Run("redirect_stub", func(t *testing.T) {
		idAlloc, cs, cat, w := newTestContentStoreDepsWithWAL(t)

		const originalFileID = uint64(1)
		putSplitRecord(t, cat, originalFileID, uint64(len(FixtureFileContent)))

		newFileIDsByPath, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, FixtureFileContent, FixtureSplitPlan)
		if err != nil {
			t.Fatalf("ExecuteSplitAllocateAndWrite: %v", err)
		}

		newFileIDs := make([]uint64, 0, len(newFileIDsByPath))
		for _, proposal := range FixtureSplitPlan.Files {
			newFileIDs = append(newFileIDs, newFileIDsByPath[proposal.NewPath])
		}

		updated, err := ExecuteSplitRedirectStub(cat, w, cs, originalFileID, newFileIDs)
		if err != nil {
			t.Fatalf("ExecuteSplitRedirectStub: %v", err)
		}

		if updated.Status != catalog.StatusRedirect {
			t.Errorf("updated.Status = %v, want catalog.StatusRedirect", updated.Status)
		}
		if !uint64SlicesEqual(updated.RedirectTargetIDs, newFileIDs) {
			t.Errorf("updated.RedirectTargetIDs = %v, want %v", updated.RedirectTargetIDs, newFileIDs)
		}

		// The catalog record itself must reflect the same values when re-fetched.
		refetched, err := cat.Get(originalFileID)
		if err != nil {
			t.Fatalf("cat.Get(originalFileID): %v", err)
		}
		if refetched.Status != catalog.StatusRedirect {
			t.Errorf("refetched.Status = %v, want catalog.StatusRedirect", refetched.Status)
		}
		if !uint64SlicesEqual(refetched.RedirectTargetIDs, newFileIDs) {
			t.Errorf("refetched.RedirectTargetIDs = %v, want %v", refetched.RedirectTargetIDs, newFileIDs)
		}

		// The stub file must have replaced the original content at the old
		// path (cs.ContentPath(originalFileID)).
		gotStub, err := os.ReadFile(cs.ContentPath(originalFileID))
		if err != nil {
			t.Fatalf("reading stub content file: %v", err)
		}
		wantStub := buildRedirectStubContent(newFileIDs)
		if !bytes.Equal(gotStub, wantStub) {
			t.Errorf("stub content = %q, want %q", gotStub, wantStub)
		}
		if refetched.SizeBytes != uint64(len(wantStub)) {
			t.Errorf("refetched.SizeBytes = %d, want %d", refetched.SizeBytes, len(wantStub))
		}
	})

	t.Run("nil_deps", func(t *testing.T) {
		_, cs, cat, w := newTestContentStoreDepsWithWAL(t)
		putSplitRecord(t, cat, 1, 10)

		if _, err := ExecuteSplitRedirectStub(nil, w, cs, 1, []uint64{2}); err == nil {
			t.Fatal("expected error for nil cat, got nil")
		}
		if _, err := ExecuteSplitRedirectStub(cat, nil, cs, 1, []uint64{2}); err == nil {
			t.Fatal("expected error for nil w, got nil")
		}
		if _, err := ExecuteSplitRedirectStub(cat, w, nil, 1, []uint64{2}); err == nil {
			t.Fatal("expected error for nil cs, got nil")
		}
	})

	t.Run("empty_targets", func(t *testing.T) {
		_, cs, cat, w := newTestContentStoreDepsWithWAL(t)
		putSplitRecord(t, cat, 1, 10)

		if _, err := ExecuteSplitRedirectStub(cat, w, cs, 1, nil); err == nil {
			t.Fatal("expected error for empty newFileIDs, got nil")
		}
	})

	t.Run("too_many_targets", func(t *testing.T) {
		_, cs, cat, w := newTestContentStoreDepsWithWAL(t)
		putSplitRecord(t, cat, 1, 10)

		tooMany := make([]uint64, catalog.MaxRedirectTargets+1)
		for i := range tooMany {
			tooMany[i] = uint64(i + 2)
		}

		if _, err := ExecuteSplitRedirectStub(cat, w, cs, 1, tooMany); err == nil {
			t.Fatal("expected error for too many redirect targets, got nil")
		}
	})

	t.Run("record_not_found", func(t *testing.T) {
		_, cs, cat, w := newTestContentStoreDepsWithWAL(t)

		if _, err := ExecuteSplitRedirectStub(cat, w, cs, 999, []uint64{2}); !errors.Is(err, catalog.ErrNotFound) {
			t.Fatalf("ExecuteSplitRedirectStub for missing fileID = %v, want wrapped ErrNotFound", err)
		}
	})

	t.Run("wrong_status", func(t *testing.T) {
		_, cs, cat, w := newTestContentStoreDepsWithWAL(t)

		if err := cat.Put(catalog.CatalogRecord{FileID: 1, Status: catalog.StatusActive}); err != nil {
			t.Fatalf("seeding StatusActive record: %v", err)
		}

		if _, err := ExecuteSplitRedirectStub(cat, w, cs, 1, []uint64{2}); !errors.Is(err, ErrNotSplit) {
			t.Fatalf("ExecuteSplitRedirectStub for StatusActive record = %v, want wrapped ErrNotSplit", err)
		}
	})
}

// uint64SlicesEqual reports whether a and b contain the same uint64s in the
// same order.
func uint64SlicesEqual(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// newTestBtree opens a fresh, isolated (t.TempDir()) index file and wraps it
// in a brand-new, empty *btree.Tree via the real production path
// (btree.OpenIndexFile / btree.NewNodeStore / btree.NewNodeAllocator /
// btree.NewTree), ready for ExecuteSplitBtreeInsert's tests (2b.3.3). The
// initial root is passed as 0 (btree's reservedNodeID -- an empty tree),
// matching the convention engine/btree's own tests use for a brand-new tree.
func newTestBtree(t *testing.T) *btree.Tree {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.idx")
	f, err := btree.OpenIndexFile(path)
	if err != nil {
		t.Fatalf("btree.OpenIndexFile: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	store := btree.NewNodeStore(f)
	alloc, err := btree.NewNodeAllocator(store)
	if err != nil {
		t.Fatalf("btree.NewNodeAllocator: %v", err)
	}
	t.Cleanup(func() {
		if err := alloc.Close(); err != nil {
			t.Errorf("NodeAllocator.Close: %v", err)
		}
	})

	return btree.NewTree(store, alloc, 0)
}

func TestSplitBtreeRepoint(t *testing.T) {
	const oldPath = "fixture-original.md"
	const fallbackOriginalFileID = uint64(1)

	t.Run("repoint", func(t *testing.T) {
		idAlloc, cs, cat, w := newTestContentStoreDepsWithWAL(t)
		tree := newTestBtree(t)

		// Allocate originalFileID via idAlloc.Next() BEFORE allocating the
		// new split-off fileIDs below, so originalFileID and the newly
		// allocated fileIDs can never collide (idAlloc.Next() hands out a
		// strictly increasing sequence starting at 1). This mirrors the
		// realistic ordering: the original file's fileID was assigned long
		// before any split ever runs.
		originalFileID, err := idAlloc.Next()
		if err != nil {
			t.Fatalf("allocating originalFileID: %v", err)
		}

		// Simulate pre-split state: oldPath already resolves to
		// originalFileID in the B+Tree, exactly as it would before any
		// split ever ran (no code elsewhere in this repo populates this
		// yet -- see architecture-discovery.md -- so the test seeds it
		// directly).
		if err := tree.Insert(oldPath, originalFileID); err != nil {
			t.Fatalf("seeding oldPath in tree: %v", err)
		}

		putSplitRecord(t, cat, originalFileID, uint64(len(FixtureFileContent)))

		newFileIDsByPath, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, FixtureFileContent, FixtureSplitPlan)
		if err != nil {
			t.Fatalf("ExecuteSplitAllocateAndWrite: %v", err)
		}

		newFileIDs := make([]uint64, 0, len(newFileIDsByPath))
		for _, proposal := range FixtureSplitPlan.Files {
			newFileIDs = append(newFileIDs, newFileIDsByPath[proposal.NewPath])
		}

		if _, err := ExecuteSplitRedirectStub(cat, w, cs, originalFileID, newFileIDs); err != nil {
			t.Fatalf("ExecuteSplitRedirectStub: %v", err)
		}

		if err := ExecuteSplitBtreeInsert(tree, oldPath, originalFileID, newFileIDsByPath); err != nil {
			t.Fatalf("ExecuteSplitBtreeInsert: %v", err)
		}

		// Old path still resolves, unchanged, to originalFileID.
		gotOldFileID, found, err := tree.Lookup(oldPath)
		if err != nil {
			t.Fatalf("tree.Lookup(oldPath): %v", err)
		}
		if !found {
			t.Fatalf("tree.Lookup(oldPath): found = false, want true")
		}
		if gotOldFileID != originalFileID {
			t.Errorf("tree.Lookup(oldPath) fileID = %d, want %d (originalFileID)", gotOldFileID, originalFileID)
		}

		// ...and that fileID's content is now the redirect stub (composing
		// with 2b.3.2), not the original file content.
		gotOldContent, err := os.ReadFile(cs.ContentPath(gotOldFileID))
		if err != nil {
			t.Fatalf("reading content at resolved old fileID: %v", err)
		}
		wantStub := buildRedirectStubContent(newFileIDs)
		if !bytes.Equal(gotOldContent, wantStub) {
			t.Errorf("content at resolved old fileID = %q, want redirect stub %q", gotOldContent, wantStub)
		}

		// Every new path resolves to its own new fileID, distinct from
		// originalFileID and from each other, and its content is the actual
		// split-off section content (composing with 2b.3.1).
		seenNewFileIDs := make(map[uint64]bool, len(FixtureSplitPlan.Files))
		for _, proposal := range FixtureSplitPlan.Files {
			gotFileID, found, err := tree.Lookup(proposal.NewPath)
			if err != nil {
				t.Fatalf("tree.Lookup(%q): %v", proposal.NewPath, err)
			}
			if !found {
				t.Fatalf("tree.Lookup(%q): found = false, want true", proposal.NewPath)
			}
			if gotFileID != newFileIDsByPath[proposal.NewPath] {
				t.Errorf("tree.Lookup(%q) fileID = %d, want %d", proposal.NewPath, gotFileID, newFileIDsByPath[proposal.NewPath])
			}
			if gotFileID == originalFileID {
				t.Errorf("tree.Lookup(%q) fileID = %d, must not equal originalFileID", proposal.NewPath, gotFileID)
			}
			if seenNewFileIDs[gotFileID] {
				t.Errorf("fileID %d resolved for more than one new path", gotFileID)
			}
			seenNewFileIDs[gotFileID] = true

			gotContent, err := os.ReadFile(cs.ContentPath(gotFileID))
			if err != nil {
				t.Fatalf("reading content at resolved new fileID for %q: %v", proposal.NewPath, err)
			}
			wantContent := extractSections(FixtureFileContent, proposal.SectionRanges)
			if !bytes.Equal(gotContent, wantContent) {
				t.Errorf("content at resolved new fileID for %q = %q, want %q", proposal.NewPath, gotContent, wantContent)
			}
		}
	})

	t.Run("nil_tree", func(t *testing.T) {
		if err := ExecuteSplitBtreeInsert(nil, oldPath, fallbackOriginalFileID, map[string]uint64{"a.md": 2}); err == nil {
			t.Fatal("expected error for nil tree, got nil")
		}
	})

	t.Run("empty_old_path", func(t *testing.T) {
		tree := newTestBtree(t)
		if err := ExecuteSplitBtreeInsert(tree, "", fallbackOriginalFileID, map[string]uint64{"a.md": 2}); err == nil {
			t.Fatal("expected error for empty oldPath, got nil")
		}
	})

	t.Run("empty_new_paths", func(t *testing.T) {
		tree := newTestBtree(t)
		if err := ExecuteSplitBtreeInsert(tree, oldPath, fallbackOriginalFileID, nil); err == nil {
			t.Fatal("expected error for nil newPathFileIDs, got nil")
		}
		if err := ExecuteSplitBtreeInsert(tree, oldPath, fallbackOriginalFileID, map[string]uint64{}); err == nil {
			t.Fatal("expected error for empty newPathFileIDs, got nil")
		}
	})

	t.Run("new_path_equals_old_path", func(t *testing.T) {
		tree := newTestBtree(t)
		if err := ExecuteSplitBtreeInsert(tree, oldPath, fallbackOriginalFileID, map[string]uint64{oldPath: 2}); err == nil {
			t.Fatal("expected error when a new path equals oldPath, got nil")
		}
	})
}

// TestSplitBtreeKeyNormalization is subtask 4.5.3.4's ("Add topic-path key
// normalization/namespace layer for B+Tree keys used by split execution")
// dedicated test, per that subtask's test spec: insert paths with
// equivalent-but-differently-formatted representations (e.g. trailing
// separators), assert they normalize to the same canonical key.
func TestSplitBtreeKeyNormalization(t *testing.T) {
	const canonicalOldPath = "notes/fixture-original.md"
	const fallbackOriginalFileID = uint64(1)

	t.Run("equivalent_forms_resolve_to_same_canonical_key", func(t *testing.T) {
		tree := newTestBtree(t)

		originalFileID := uint64(10)
		newFileIDA := uint64(11)
		newFileIDB := uint64(12)

		// oldPath supplied in a differently-formatted-but-equivalent form:
		// backslash separator, a leading "./", and a trailing slash.
		rawOldPath := `./notes\fixture-original.md/`

		newPathFileIDs := map[string]uint64{
			// Trailing separator (the test spec's explicit example).
			"notes/a.md/": newFileIDA,
			// Doubled slash, collapses to a single separator.
			"notes//b.md": newFileIDB,
		}

		if err := ExecuteSplitBtreeInsert(tree, rawOldPath, originalFileID, newPathFileIDs); err != nil {
			t.Fatalf("ExecuteSplitBtreeInsert: %v", err)
		}

		// The canonical form of oldPath resolves to originalFileID...
		gotOldFileID, found, err := tree.Lookup(canonicalOldPath)
		if err != nil {
			t.Fatalf("tree.Lookup(%q): %v", canonicalOldPath, err)
		}
		if !found {
			t.Fatalf("tree.Lookup(%q): found = false, want true", canonicalOldPath)
		}
		if gotOldFileID != originalFileID {
			t.Errorf("tree.Lookup(%q) fileID = %d, want %d", canonicalOldPath, gotOldFileID, originalFileID)
		}

		// ...while the raw, differently-formatted form supplied to
		// ExecuteSplitBtreeInsert was never itself used as a literal key: it
		// was canonicalized before insertion, so looking it up VERBATIM does
		// not resolve (btree.Tree.Lookup itself performs no normalization --
		// only insertion, via normalizeTopicPath, does -- see that function's
		// doc comment for why retrofitting Lookup is out of this subtask's
		// scope).
		if _, found, err := tree.Lookup(rawOldPath); err != nil {
			t.Fatalf("tree.Lookup(rawOldPath): %v", err)
		} else if found {
			t.Errorf("tree.Lookup(rawOldPath) found = true for un-normalized raw key %q, want false (only the canonical form should be an actual B+Tree key)", rawOldPath)
		}

		wantNewFileIDs := map[string]uint64{
			"notes/a.md": newFileIDA,
			"notes/b.md": newFileIDB,
		}
		for canonicalNewPath, wantFileID := range wantNewFileIDs {
			gotFileID, found, err := tree.Lookup(canonicalNewPath)
			if err != nil {
				t.Fatalf("tree.Lookup(%q): %v", canonicalNewPath, err)
			}
			if !found {
				t.Fatalf("tree.Lookup(%q): found = false, want true", canonicalNewPath)
			}
			if gotFileID != wantFileID {
				t.Errorf("tree.Lookup(%q) fileID = %d, want %d", canonicalNewPath, gotFileID, wantFileID)
			}
		}
	})

	t.Run("normalized_new_path_equal_to_normalized_old_path_is_rejected", func(t *testing.T) {
		tree := newTestBtree(t)

		// "notes/dup.md/" and "notes\\dup.md" both normalize to
		// "notes/dup.md": inserting one as oldPath and the other as a
		// newPath must still be rejected as "new path must not equal
		// oldPath", even though the raw strings differ.
		rawOldPath := "notes/dup.md/"
		rawNewPath := `notes\dup.md`

		if err := ExecuteSplitBtreeInsert(tree, rawOldPath, fallbackOriginalFileID, map[string]uint64{rawNewPath: 2}); err == nil {
			t.Fatal("expected error when a new path normalizes to the same canonical key as oldPath, got nil")
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		for _, p := range []string{
			"a/b.md",
			"a/b.md/",
			`a\b.md`,
			"./a/b.md",
			"a//b.md",
			"./a//b.md/",
		} {
			normalizedOnce := normalizeTopicPath(p)
			normalizedTwice := normalizeTopicPath(normalizedOnce)
			if normalizedOnce != normalizedTwice {
				t.Errorf("normalizeTopicPath(%q) = %q, but normalizing it again gave %q, want idempotent", p, normalizedOnce, normalizedTwice)
			}
			if normalizedOnce != "a/b.md" {
				t.Errorf("normalizeTopicPath(%q) = %q, want %q", p, normalizedOnce, "a/b.md")
			}
		}
	})
}

// newTestEdgeAppender opens a fresh graph.EdgeAppender rooted at a
// t.TempDir() subdirectory, for TestSplitGraphEdges's use.
func newTestEdgeAppender(t *testing.T) *graph.EdgeAppender {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "edges")
	appender, err := graph.OpenEdgeAppender(dir)
	if err != nil {
		t.Fatalf("graph.OpenEdgeAppender: %v", err)
	}
	t.Cleanup(func() {
		if err := appender.Close(); err != nil {
			t.Errorf("EdgeAppender.Close: %v", err)
		}
	})
	return appender
}

// hasEdge reports whether edges contains an Edge exactly matching want.
func hasEdge(edges []graph.Edge, want graph.Edge) bool {
	for _, e := range edges {
		if e == want {
			return true
		}
	}
	return false
}

func TestSplitGraphEdges(t *testing.T) {
	const originalFileID = uint64(1)
	const inboundSourceFileID = uint64(999) // some other file, pre-existing edge into originalFileID

	t.Run("graph_edges", func(t *testing.T) {
		idAlloc, cs, cat, w := newTestContentStoreDepsWithWAL(t)

		dir := filepath.Join(t.TempDir(), "edges")
		appender, err := graph.OpenEdgeAppender(dir)
		if err != nil {
			t.Fatalf("graph.OpenEdgeAppender: %v", err)
		}

		// Seed a pre-existing inbound edge that points at the old path's
		// fileID, BEFORE the split ever runs -- simulating some earlier,
		// unrelated graph relationship (e.g. a reference/citation edge)
		// that targeted the file that is about to be split.
		preExistingInbound := graph.Edge{Source: inboundSourceFileID, Target: originalFileID, Type: graph.EdgeSplitSibling}
		if err := appender.AppendEdge(preExistingInbound); err != nil {
			t.Fatalf("seeding pre-existing inbound edge: %v", err)
		}

		putSplitRecord(t, cat, originalFileID, uint64(len(FixtureFileContent)))

		newFileIDsByPath, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, FixtureFileContent, FixtureSplitPlan)
		if err != nil {
			t.Fatalf("ExecuteSplitAllocateAndWrite: %v", err)
		}

		newFileIDs := make([]uint64, 0, len(newFileIDsByPath))
		for _, proposal := range FixtureSplitPlan.Files {
			newFileIDs = append(newFileIDs, newFileIDsByPath[proposal.NewPath])
		}

		if _, err := ExecuteSplitRedirectStub(cat, w, cs, originalFileID, newFileIDs); err != nil {
			t.Fatalf("ExecuteSplitRedirectStub: %v", err)
		}

		if err := ExecuteSplitGraphEdges(appender, originalFileID, newFileIDs); err != nil {
			t.Fatalf("ExecuteSplitGraphEdges: %v", err)
		}
		if err := appender.Close(); err != nil {
			t.Fatalf("EdgeAppender.Close: %v", err)
		}

		gotEdges, err := graph.ReadAll(dir)
		if err != nil {
			t.Fatalf("graph.ReadAll: %v", err)
		}

		// The pre-existing inbound edge must still be present, byte-for-byte
		// unchanged: engine/graph is append-only and offers no rewrite API,
		// and because 2b.3.2 reuses originalFileID for the redirect stub,
		// this unchanged edge already points at the stub -- nothing needed
		// to be rewritten or re-appended for it to do so.
		if !hasEdge(gotEdges, preExistingInbound) {
			t.Errorf("pre-existing inbound edge %+v missing from ReadAll output; append-only log must never rewrite/drop existing edges", preExistingInbound)
		}

		// SPLIT_SIBLING edges: both directions, for every pair of new
		// fileIDs (complete directed graph; see architecture-discovery.md).
		for _, a := range newFileIDs {
			for _, b := range newFileIDs {
				if a == b {
					continue
				}
				want := graph.Edge{Source: a, Target: b, Type: graph.EdgeSplitSibling}
				if !hasEdge(gotEdges, want) {
					t.Errorf("missing SPLIT_SIBLING edge %+v", want)
				}
			}
		}

		// EdgeRedirect edges: from the (identity-reused) originalFileID/stub
		// to each new fileID.
		for _, id := range newFileIDs {
			want := graph.Edge{Source: originalFileID, Target: id, Type: graph.EdgeRedirect}
			if !hasEdge(gotEdges, want) {
				t.Errorf("missing REDIRECT edge %+v", want)
			}
		}

		// Sanity: total edge count is exactly 1 (pre-existing inbound) +
		// N*(N-1) (siblings) + N (redirects), for N = len(newFileIDs).
		n := len(newFileIDs)
		wantCount := 1 + n*(n-1) + n
		if len(gotEdges) != wantCount {
			t.Errorf("len(gotEdges) = %d, want %d", len(gotEdges), wantCount)
		}
	})

	t.Run("nil_appender", func(t *testing.T) {
		if err := ExecuteSplitGraphEdges(nil, originalFileID, []uint64{2, 3}); err == nil {
			t.Fatal("expected error for nil appender, got nil")
		}
	})

	t.Run("empty_new_file_ids", func(t *testing.T) {
		appender := newTestEdgeAppender(t)
		if err := ExecuteSplitGraphEdges(appender, originalFileID, nil); err == nil {
			t.Fatal("expected error for nil newFileIDs, got nil")
		}
		if err := ExecuteSplitGraphEdges(appender, originalFileID, []uint64{}); err == nil {
			t.Fatal("expected error for empty newFileIDs, got nil")
		}
	})

	t.Run("single_new_file", func(t *testing.T) {
		// A degenerate split into exactly one new file: no SPLIT_SIBLING
		// edges are possible (no pair exists), only the REDIRECT edge.
		appender := newTestEdgeAppender(t)
		if err := ExecuteSplitGraphEdges(appender, originalFileID, []uint64{42}); err != nil {
			t.Fatalf("ExecuteSplitGraphEdges: %v", err)
		}
	})
}

// putSplittingRecord seeds a CatalogRecord for fileID directly via cat.Put
// with Status = catalog.StatusSplitting, simulating the state a preceding,
// successful Orchestrator.BeginSplit(fileID) call (2b.1.3) would have left
// behind before ExecuteSplitAtomic (2b.3.6) is ever called -- deliberately
// StatusSplitting, not StatusSplit (contrast putSplitRecord above): see
// ExecuteSplitAtomic's doc comment for why this subtask's precondition is
// StatusSplitting, not StatusSplit.
func putSplittingRecord(t *testing.T, cat *catalog.Catalog, fileID uint64, sizeBytes uint64) {
	t.Helper()
	if err := cat.Put(catalog.CatalogRecord{
		FileID:         fileID,
		CurrentVersion: 0,
		SizeBytes:      sizeBytes,
		Status:         catalog.StatusSplitting,
	}); err != nil {
		t.Fatalf("seeding StatusSplitting record for fileID %d: %v", fileID, err)
	}
}

// atomicCommitTestDeps bundles every dependency ExecuteSplitAtomic and
// RecoverSplitCommits need, all rooted at isolated t.TempDir() locations, for
// TestSplitAtomicCommit's use.
type atomicCommitTestDeps struct {
	idAlloc  *catalog.IDAllocator
	cs       *catalog.ContentStore
	cat      *catalog.Catalog
	w        *wal.Writer
	walDir   string
	tree     *btree.Tree
	appender *graph.EdgeAppender
	guard    *FileGuard
}

// setAtomicCommitHook installs hook as atomicCommitHook for the duration of
// the calling (sub)test, automatically restoring it to nil via t.Cleanup.
// Centralizing this avoids any test accidentally leaking a hook into a
// later, unrelated subtest.
func setAtomicCommitHook(t *testing.T, hook func(stage string) error) {
	t.Helper()
	atomicCommitHook = hook
	t.Cleanup(func() { atomicCommitHook = nil })
}

// assertFullSplitApplied asserts that deps reflects the FULLY-applied
// end-state of a split from oldPath/originalFileID into newFileIDsByPath,
// regardless of whether that state was reached via a single uninterrupted
// ExecuteSplitAtomic call or via RecoverSplitCommits completing a
// crash-interrupted one.
func assertFullSplitApplied(t *testing.T, deps atomicCommitTestDeps, oldPath string, originalFileID uint64, newFileIDsByPath map[string]uint64) {
	t.Helper()

	rec, err := deps.cat.Get(originalFileID)
	if err != nil {
		t.Fatalf("cat.Get(originalFileID): %v", err)
	}
	if rec.Status != catalog.StatusRedirect {
		t.Errorf("rec.Status = %v, want catalog.StatusRedirect", rec.Status)
	}
	if len(rec.RedirectTargetIDs) != len(newFileIDsByPath) {
		t.Errorf("len(rec.RedirectTargetIDs) = %d, want %d", len(rec.RedirectTargetIDs), len(newFileIDsByPath))
	}

	gotOldFileID, found, err := deps.tree.Lookup(oldPath)
	if err != nil {
		t.Fatalf("tree.Lookup(oldPath): %v", err)
	}
	if !found || gotOldFileID != originalFileID {
		t.Errorf("tree.Lookup(oldPath) = (%d, %v), want (%d, true)", gotOldFileID, found, originalFileID)
	}

	newFileIDs := make([]uint64, 0, len(newFileIDsByPath))
	for newPath, fileID := range newFileIDsByPath {
		gotFileID, found, err := deps.tree.Lookup(newPath)
		if err != nil {
			t.Fatalf("tree.Lookup(%q): %v", newPath, err)
		}
		if !found || gotFileID != fileID {
			t.Errorf("tree.Lookup(%q) = (%d, %v), want (%d, true)", newPath, gotFileID, found, fileID)
		}

		// Regression coverage for issue #14's (2b.5) bugfix: every new
		// fileID produced by a split must have its OWN catalog.CatalogRecord
		// (Status=StatusActive), not just a B+Tree entry and content file.
		// Before the fix, cat.Get(fileID) here returned ErrNotFound for
		// every split-off file.
		newCatRec, err := deps.cat.Get(fileID)
		if err != nil {
			t.Errorf("cat.Get(new fileID %d, %q): %v", fileID, newPath, err)
		} else if newCatRec.Status != catalog.StatusActive {
			t.Errorf("cat.Get(new fileID %d, %q).Status = %v, want StatusActive", fileID, newPath, newCatRec.Status)
		}

		newFileIDs = append(newFileIDs, fileID)
	}

	sort.Slice(newFileIDs, func(i, j int) bool { return newFileIDs[i] < newFileIDs[j] })
	for i := range newFileIDs {
		for j := range newFileIDs {
			if i == j {
				continue
			}
			if !edgeExists(t, deps.appender, graph.Edge{Source: newFileIDs[i], Target: newFileIDs[j], Type: graph.EdgeSplitSibling}) {
				t.Errorf("missing SPLIT_SIBLING edge %d->%d", newFileIDs[i], newFileIDs[j])
			}
		}
	}
	for _, id := range newFileIDs {
		if !edgeExists(t, deps.appender, graph.Edge{Source: originalFileID, Target: id, Type: graph.EdgeRedirect}) {
			t.Errorf("missing REDIRECT edge %d->%d", originalFileID, id)
		}
	}
}

// edgeExists reads back appender's own directory (via graph.ReadAll) and
// reports whether want is present exactly (at least once; duplicate-freedom
// is asserted separately by edgeCount).
func edgeExists(t *testing.T, appender *graph.EdgeAppender, want graph.Edge) bool {
	t.Helper()
	edges := readAppenderEdges(t, appender)
	return hasEdge(edges, want)
}

// edgeCount reads back appender's own directory and returns how many times
// want appears -- used to assert idempotent replay never duplicates edges.
func edgeCount(t *testing.T, appender *graph.EdgeAppender, want graph.Edge) int {
	t.Helper()
	edges := readAppenderEdges(t, appender)
	n := 0
	for _, e := range edges {
		if e == want {
			n++
		}
	}
	return n
}

// appenderDirs tracks each *graph.EdgeAppender's backing directory so
// edgeExists/edgeCount can call graph.ReadAll without EdgeAppender exposing
// its directory publicly.
var appenderDirs = map[*graph.EdgeAppender]string{}

func readAppenderEdges(t *testing.T, appender *graph.EdgeAppender) []graph.Edge {
	t.Helper()
	dir, ok := appenderDirs[appender]
	if !ok {
		t.Fatalf("no known directory for appender %p; use newTestEdgeAppenderTracked", appender)
	}
	edges, err := graph.ReadAll(dir)
	if err != nil {
		t.Fatalf("graph.ReadAll(%s): %v", dir, err)
	}
	return edges
}

// newTestEdgeAppenderTracked is newTestEdgeAppender, additionally recording
// the appender's backing directory in appenderDirs so
// edgeExists/edgeCount/readAppenderEdges can read it back.
func newTestEdgeAppenderTracked(t *testing.T) *graph.EdgeAppender {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "edges")
	appender, err := graph.OpenEdgeAppender(dir)
	if err != nil {
		t.Fatalf("graph.OpenEdgeAppender: %v", err)
	}
	t.Cleanup(func() {
		if err := appender.Close(); err != nil {
			t.Errorf("EdgeAppender.Close: %v", err)
		}
		delete(appenderDirs, appender)
	})
	appenderDirs[appender] = dir
	return appender
}

func TestSplitAtomicCommit(t *testing.T) {
	const oldPath = "fixture-original.md"

	// newDeps is a small local helper composing newAtomicCommitTestDeps with
	// newTestEdgeAppenderTracked (rather than newTestEdgeAppender), so this
	// test's own assertions can read back the appender's edges.
	newDeps := func(t *testing.T, originalFileID uint64) atomicCommitTestDeps {
		t.Helper()
		idAlloc, cs, cat, w := newTestContentStoreDepsWithWAL(t)
		// newTestContentStoreDepsWithWAL roots w at "<root>/wal" (a sibling
		// of ContentStore's own "<root>/content" directory, NOT nested
		// beneath it) -- see newTestWAL's own doc comment. ContentPath's
		// parent directory is therefore "<root>/content", so walDir must
		// walk up one more level before appending "wal".
		walDir := filepath.Join(filepath.Dir(filepath.Dir(cs.ContentPath(0))), "wal")

		// Burn idAlloc's cursor up to (and including) originalFileID's
		// hardcoded constant, so ExecuteSplitAtomic's own idAlloc.Next()
		// calls for NEW fileIDs can never collide with it. In real
		// production every fileID -- including one that later gets
		// split -- always originates from this SAME idAlloc sequence, so
		// this collision can never happen outside a test fixture that
		// (like this one) assigns originalFileID a bespoke constant instead
		// of obtaining it via idAlloc.Next() first. Surfaced by issue #14's
		// (2b.5) bugfix: once ExecuteSplitAtomic started actually cat.Put-ing
		// a record for each new fileID, this latent fixture collision (a new
		// fileID silently reusing originalFileID's own value and clobbering
		// its just-committed StatusRedirect record) became visible as a real
		// test failure.
		for {
			id, err := idAlloc.Next()
			if err != nil {
				t.Fatalf("burning idAlloc IDs up to originalFileID: %v", err)
			}
			if id >= originalFileID {
				break
			}
		}

		tree := newTestBtree(t)
		if err := tree.Insert(oldPath, originalFileID); err != nil {
			t.Fatalf("seeding oldPath in tree: %v", err)
		}

		appender := newTestEdgeAppenderTracked(t)
		guard := NewFileGuard()
		guard.TryAcquire(originalFileID)

		putSplittingRecord(t, cat, originalFileID, uint64(len(FixtureFileContent)))

		return atomicCommitTestDeps{
			idAlloc:  idAlloc,
			cs:       cs,
			cat:      cat,
			w:        w,
			walDir:   walDir,
			tree:     tree,
			appender: appender,
			guard:    guard,
		}
	}

	t.Run("happy_path_commits_atomically_and_releases_guard", func(t *testing.T) {
		const originalFileID = uint64(1)
		deps := newDeps(t, originalFileID)

		updated, err := ExecuteSplitAtomic(deps.idAlloc, deps.cat, deps.cs, deps.tree, deps.appender, deps.w, deps.guard, oldPath, originalFileID, FixtureFileContent, FixtureSplitPlan)
		if err != nil {
			t.Fatalf("ExecuteSplitAtomic: %v", err)
		}
		if updated.Status != catalog.StatusRedirect {
			t.Errorf("updated.Status = %v, want catalog.StatusRedirect", updated.Status)
		}

		newFileIDsByPath := make(map[string]uint64, len(FixtureSplitPlan.Files))
		for i, proposal := range FixtureSplitPlan.Files {
			newFileIDsByPath[proposal.NewPath] = updated.RedirectTargetIDs[i]
		}
		assertFullSplitApplied(t, deps, oldPath, originalFileID, newFileIDsByPath)

		// "Release queued writers on commit": the guard must be released,
		// and the catalog record's Status must no longer be StatusSplitting
		// (the actual AdmitWrite gate), both once the transaction commits.
		if deps.guard.InProgress(originalFileID) {
			t.Error("guard.InProgress(originalFileID) = true after successful commit, want false (released on commit)")
		}
		if updated.Status == catalog.StatusSplitting {
			t.Error("updated.Status is still StatusSplitting after successful commit; writers would remain incorrectly blocked")
		}
	})

	t.Run("nil_and_precondition_checks", func(t *testing.T) {
		const originalFileID = uint64(1)
		deps := newDeps(t, originalFileID)

		if _, err := ExecuteSplitAtomic(nil, deps.cat, deps.cs, deps.tree, deps.appender, deps.w, deps.guard, oldPath, originalFileID, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Error("expected error for nil idAlloc, got nil")
		}
		if _, err := ExecuteSplitAtomic(deps.idAlloc, nil, deps.cs, deps.tree, deps.appender, deps.w, deps.guard, oldPath, originalFileID, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Error("expected error for nil cat, got nil")
		}
		if _, err := ExecuteSplitAtomic(deps.idAlloc, deps.cat, nil, deps.tree, deps.appender, deps.w, deps.guard, oldPath, originalFileID, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Error("expected error for nil cs, got nil")
		}
		if _, err := ExecuteSplitAtomic(deps.idAlloc, deps.cat, deps.cs, nil, deps.appender, deps.w, deps.guard, oldPath, originalFileID, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Error("expected error for nil tree, got nil")
		}
		if _, err := ExecuteSplitAtomic(deps.idAlloc, deps.cat, deps.cs, deps.tree, nil, deps.w, deps.guard, oldPath, originalFileID, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Error("expected error for nil appender, got nil")
		}
		if _, err := ExecuteSplitAtomic(deps.idAlloc, deps.cat, deps.cs, deps.tree, deps.appender, nil, deps.guard, oldPath, originalFileID, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Error("expected error for nil w, got nil")
		}
		if _, err := ExecuteSplitAtomic(deps.idAlloc, deps.cat, deps.cs, deps.tree, deps.appender, deps.w, nil, oldPath, originalFileID, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Error("expected error for nil guard, got nil")
		}
		if _, err := ExecuteSplitAtomic(deps.idAlloc, deps.cat, deps.cs, deps.tree, deps.appender, deps.w, deps.guard, "", originalFileID, FixtureFileContent, FixtureSplitPlan); err == nil {
			t.Error("expected error for empty oldPath, got nil")
		}

		// StatusActive (never SPLITTING at all) must be refused too.
		const activeFileID = uint64(2)
		if err := deps.cat.Put(catalog.CatalogRecord{FileID: activeFileID, Status: catalog.StatusActive}); err != nil {
			t.Fatalf("seeding StatusActive record: %v", err)
		}
		if _, err := ExecuteSplitAtomic(deps.idAlloc, deps.cat, deps.cs, deps.tree, deps.appender, deps.w, deps.guard, oldPath, activeFileID, FixtureFileContent, FixtureSplitPlan); !errors.Is(err, ErrNotSplitting) {
			t.Errorf("ExecuteSplitAtomic for StatusActive record = %v, want wrapped ErrNotSplitting", err)
		}
	})

	// crashPointTest exercises one of ExecuteSplitAtomic's documented
	// mid-transaction crash points (see atomicCommitHook's doc comment): it
	// injects a deterministic simulated crash at hookStage, asserts the
	// documented pre-recovery invariant for that stage (either "nothing
	// durable happened" for the one point before the commit's fsync, or
	// "durable but not yet applied" for every point after it), then calls
	// RecoverSplitCommits and asserts the split's full, exact effect is
	// present afterward -- proving recovery converges on the same
	// fully-applied state regardless of exactly where the simulated crash
	// landed.
	crashPointTest := func(hookStage string, wantPreRecoveryStatus catalog.RecordStatus) func(t *testing.T) {
		return func(t *testing.T) {
			const originalFileID = uint64(1)
			deps := newDeps(t, originalFileID)

			simulatedCrash := errors.New("simulated crash")
			setAtomicCommitHook(t, func(stage string) error {
				if stage == hookStage {
					return simulatedCrash
				}
				return nil
			})

			_, err := ExecuteSplitAtomic(deps.idAlloc, deps.cat, deps.cs, deps.tree, deps.appender, deps.w, deps.guard, oldPath, originalFileID, FixtureFileContent, FixtureSplitPlan)
			if !errors.Is(err, simulatedCrash) {
				t.Fatalf("ExecuteSplitAtomic with simulated crash at %q = %v, want wrapped simulatedCrash", hookStage, err)
			}

			// Pre-recovery: catalog status must match what this stage's
			// documented guarantee promises (StatusSplitting/unchanged for
			// the before-commit stage, since the transition to
			// StatusRedirect only happens inside the same atomic apply the
			// commit record's fsync gates).
			rec, getErr := deps.cat.Get(originalFileID)
			if getErr != nil {
				t.Fatalf("cat.Get(originalFileID) pre-recovery: %v", getErr)
			}
			if rec.Status != wantPreRecoveryStatus {
				t.Errorf("pre-recovery rec.Status = %v, want %v", rec.Status, wantPreRecoveryStatus)
			}

			// The guard must NOT have been released: the transaction did
			// not fully apply, so a fresh split attempt must still be
			// refused.
			if !deps.guard.InProgress(originalFileID) {
				t.Error("guard.InProgress(originalFileID) = false after an incomplete commit, want true (guard must not be released early)")
			}

			// Recovery must converge on the fully-applied state regardless
			// of exactly where the simulated crash landed, EXCEPT for the
			// one stage before the commit record was ever appended, where
			// there is nothing to recover and the pre-split state must
			// remain fully, exactly intact.
			if err := RecoverSplitCommits(deps.walDir, deps.cat, deps.tree, deps.appender); err != nil {
				t.Fatalf("RecoverSplitCommits: %v", err)
			}

			if hookStage == "before_commit_append" {
				rec, err := deps.cat.Get(originalFileID)
				if err != nil {
					t.Fatalf("cat.Get(originalFileID) post-recovery: %v", err)
				}
				if rec.Status != catalog.StatusSplitting {
					t.Errorf("post-recovery (no-op expected) rec.Status = %v, want catalog.StatusSplitting", rec.Status)
				}
				gotOldFileID, found, err := deps.tree.Lookup(oldPath)
				if err != nil || !found || gotOldFileID != originalFileID {
					t.Errorf("tree.Lookup(oldPath) post-recovery (no-op expected) = (%d, %v, %v), want (%d, true, nil)", gotOldFileID, found, err, originalFileID)
				}
				edges := readAppenderEdges(t, deps.appender)
				if len(edges) != 0 {
					t.Errorf("post-recovery (no-op expected) edges = %v, want none", edges)
				}
				return
			}

			// Every other stage: recovery must have completed the full
			// split.
			rec, err = deps.cat.Get(originalFileID)
			if err != nil {
				t.Fatalf("cat.Get(originalFileID) post-recovery: %v", err)
			}
			newFileIDsByPath := make(map[string]uint64, len(FixtureSplitPlan.Files))
			for i, proposal := range FixtureSplitPlan.Files {
				if i >= len(rec.RedirectTargetIDs) {
					t.Fatalf("rec.RedirectTargetIDs too short: %v", rec.RedirectTargetIDs)
				}
				newFileIDsByPath[proposal.NewPath] = rec.RedirectTargetIDs[i]
			}
			assertFullSplitApplied(t, deps, oldPath, originalFileID, newFileIDsByPath)

			// Idempotency: running recovery again must not duplicate
			// anything (extra btree inserts are naturally upserts; graph
			// edges specifically rely on AppendEdgeIfAbsent).
			if err := RecoverSplitCommits(deps.walDir, deps.cat, deps.tree, deps.appender); err != nil {
				t.Fatalf("second RecoverSplitCommits: %v", err)
			}
			for newPath, id := range newFileIDsByPath {
				_ = newPath
				if n := edgeCount(t, deps.appender, graph.Edge{Source: rec.FileID, Target: id, Type: graph.EdgeRedirect}); n != 1 {
					t.Errorf("REDIRECT edge %d->%d appears %d times after two recoveries, want exactly 1", rec.FileID, id, n)
				}
			}
		}
	}

	t.Run("crash_before_commit_append_no_visible_effect", crashPointTest("before_commit_append", catalog.StatusSplitting))
	t.Run("crash_after_commit_before_catalog_put_recovers_fully", crashPointTest("after_commit_before_catalog_put", catalog.StatusSplitting))
	t.Run("crash_after_catalog_put_before_btree_recovers_fully", crashPointTest("after_catalog_put_before_btree", catalog.StatusRedirect))
	t.Run("crash_after_btree_before_graph_recovers_fully", crashPointTest("after_btree_before_graph", catalog.StatusRedirect))

	t.Run("recover_split_commits_nil_checks", func(t *testing.T) {
		const originalFileID = uint64(1)
		deps := newDeps(t, originalFileID)
		if err := RecoverSplitCommits(deps.walDir, nil, deps.tree, deps.appender); err == nil {
			t.Error("expected error for nil cat, got nil")
		}
		if err := RecoverSplitCommits(deps.walDir, deps.cat, nil, deps.appender); err == nil {
			t.Error("expected error for nil tree, got nil")
		}
		if err := RecoverSplitCommits(deps.walDir, deps.cat, deps.tree, nil); err == nil {
			t.Error("expected error for nil appender, got nil")
		}
	})

	t.Run("recover_split_commits_empty_wal_dir_is_noop", func(t *testing.T) {
		const originalFileID = uint64(1)
		deps := newDeps(t, originalFileID)
		if err := RecoverSplitCommits(deps.walDir, deps.cat, deps.tree, deps.appender); err != nil {
			t.Fatalf("RecoverSplitCommits on empty WAL dir: %v", err)
		}
		rec, err := deps.cat.Get(originalFileID)
		if err != nil {
			t.Fatalf("cat.Get: %v", err)
		}
		if rec.Status != catalog.StatusSplitting {
			t.Errorf("rec.Status = %v, want catalog.StatusSplitting (unchanged)", rec.Status)
		}
	})
}

// TestSectionIndexInvalidation is issue #13's subtask 2b.4.1 literal test spec: perform
// a split (via ExecuteSplitAtomic, the production commit path), then immediately issue
// a ReadPartial-style offset read against the old (now redirect-stub) fileID and each
// new fileID, asserting the returned header offsets reflect post-split content only --
// never a cache entry computed against the pre-split original content.
func TestSectionIndexInvalidation(t *testing.T) {
	const oldPath = "old/topic.md"

	idAlloc, cs, cat, w := newTestContentStoreDepsWithWAL(t)
	tree := newTestBtree(t)
	appender := newTestEdgeAppenderTracked(t)

	// Allocate originalFileID via idAlloc.Next() (not a hardcoded constant), matching
	// TestSplitAtomicCommit's own established convention: this guarantees the new
	// fileIDs ExecuteSplitAtomic allocates below can never collide with originalFileID.
	originalFileID, err := idAlloc.Next()
	if err != nil {
		t.Fatalf("idAlloc.Next() (originalFileID): %v", err)
	}

	guard := NewFileGuard()
	if !guard.TryAcquire(originalFileID) {
		t.Fatalf("guard.TryAcquire: expected success")
	}

	part1 := []byte("# Part1 Header\nbody1\n") // 22 bytes
	part2 := []byte("# Part2 Header\nbody2\n") // 22 bytes
	originalContent := append(append([]byte{}, part1...), part2...)

	rec := catalog.CatalogRecord{
		FileID:    originalFileID,
		Status:    catalog.StatusSplitting,
		SizeBytes: uint64(len(originalContent)),
	}
	if _, err := cs.Create(rec, originalContent); err != nil {
		t.Fatalf("cs.Create(original): %v", err)
	}

	// Populate the cache against the PRE-split original content, so this test actually
	// exercises invalidation (not just "the cache happened to never be populated").
	preSplit, err := cs.ReadPartial(originalFileID)
	if err != nil {
		t.Fatalf("ReadPartial (pre-split): %v", err)
	}
	wantPreSplit := []catalog.HeaderOffset{
		{Header: "# Part1 Header", Offset: 0},
		{Header: "# Part2 Header", Offset: len(part1)},
	}
	if !reflect.DeepEqual(preSplit, wantPreSplit) {
		t.Fatalf("ReadPartial (pre-split) = %+v, want %+v", preSplit, wantPreSplit)
	}

	plan := SplitPlan{
		Files: []SplitFileProposal{
			{NewPath: "new/part-1.md", SectionRanges: []SectionRange{{Start: 0, End: len(part1)}}},
			{NewPath: "new/part-2.md", SectionRanges: []SectionRange{{Start: len(part1), End: len(originalContent)}}},
		},
		RedirectSummary: "split into new/part-1.md and new/part-2.md",
	}

	updated, err := ExecuteSplitAtomic(idAlloc, cat, cs, tree, appender, w, guard, oldPath, originalFileID, originalContent, plan)
	if err != nil {
		t.Fatalf("ExecuteSplitAtomic: %v", err)
	}
	if len(updated.RedirectTargetIDs) != 2 {
		t.Fatalf("updated.RedirectTargetIDs = %v, want 2 entries", updated.RedirectTargetIDs)
	}

	// ExecuteSplitAtomic returns the new fileIDs in updated.RedirectTargetIDs, in the
	// same order as plan.Files (see ExecuteSplitRedirectStub's canonical-ordering
	// contract) -- use that directly rather than re-deriving via tree.Lookup.
	if len(updated.RedirectTargetIDs) != 2 {
		t.Fatalf("len(updated.RedirectTargetIDs) = %d, want 2", len(updated.RedirectTargetIDs))
	}
	newFileID1 := updated.RedirectTargetIDs[0]
	newFileID2 := updated.RedirectTargetIDs[1]

	// ExecuteSplitAtomic deliberately does not create a CatalogRecord for either new
	// fileID (see ExecuteSplitAllocateAndWrite's doc comment: catalog visibility for
	// new fileIDs is a separate, not-yet-landed concern, pre-existing and out of scope
	// for issue #13). ReadPartial, like Read, resolves fileID through the catalog
	// first -- put minimal StatusActive records here purely so this test can exercise
	// ReadPartial's cache-correctness behavior for the new fileIDs' actual on-disk
	// content, independent of that separate, already-tracked gap.
	if err := cat.Put(catalog.CatalogRecord{FileID: newFileID1, Status: catalog.StatusActive, SizeBytes: uint64(len(part1))}); err != nil {
		t.Fatalf("cat.Put(newFileID1): %v", err)
	}
	if err := cat.Put(catalog.CatalogRecord{FileID: newFileID2, Status: catalog.StatusActive, SizeBytes: uint64(len(part2))}); err != nil {
		t.Fatalf("cat.Put(newFileID2): %v", err)
	}

	// The old fileID's content is now the redirect stub, which contains no ATX header
	// lines at all -- ReadPartial must reflect that, not the pre-split cache entry
	// populated above.
	postSplitOld, err := cs.ReadPartial(originalFileID)
	if err != nil {
		t.Fatalf("ReadPartial (old fileID, post-split): %v", err)
	}
	if len(postSplitOld) != 0 {
		t.Fatalf("ReadPartial (old fileID, post-split) = %+v, want empty (redirect-stub content has no ATX headers; stale pre-split cache was not invalidated)", postSplitOld)
	}

	postSplitNew1, err := cs.ReadPartial(newFileID1)
	if err != nil {
		t.Fatalf("ReadPartial (new fileID 1): %v", err)
	}
	wantNew1 := []catalog.HeaderOffset{{Header: "# Part1 Header", Offset: 0}}
	if !reflect.DeepEqual(postSplitNew1, wantNew1) {
		t.Fatalf("ReadPartial (new fileID 1) = %+v, want %+v", postSplitNew1, wantNew1)
	}

	postSplitNew2, err := cs.ReadPartial(newFileID2)
	if err != nil {
		t.Fatalf("ReadPartial (new fileID 2): %v", err)
	}
	wantNew2 := []catalog.HeaderOffset{{Header: "# Part2 Header", Offset: 0}}
	if !reflect.DeepEqual(postSplitNew2, wantNew2) {
		t.Fatalf("ReadPartial (new fileID 2) = %+v, want %+v", postSplitNew2, wantNew2)
	}
}

// TestSectionIndexInvalidationConcurrent is issue #13's CHANGES_REQUESTED
// fix-cycle regression test (verification adversarial-check 3): unlike
// TestSectionIndexInvalidation above (entirely serial: split fully completes,
// THEN ReadPartial is called), this test runs a real background goroutine
// hammering ReadPartial(originalFileID) concurrently with ExecuteSplitAtomic
// running the split in the main goroutine, synchronized via a start barrier,
// under -race.
//
// It specifically targets Bug 1 from the CHANGES_REQUESTED verdict: prior to
// the fix, ExecuteSplitAtomic's stub-content-write + cat.Put +
// InvalidateHeaderCache sequence took no lock at all, so a ReadPartial call
// already mid-flight with a pre-split cache entry could return that stale
// entry in the narrow window between cat.Put committing the new
// Status=Redirect record and InvalidateHeaderCache evicting the cache -- even
// though that racy call's own *return* can happen at essentially any later
// wall-clock time, including after ExecuteSplitAtomic itself has returned.
// (A naive test that only starts issuing ReadPartial calls *after*
// ExecuteSplitAtomic returns cannot catch this: by definition,
// InvalidateHeaderCache has already run by then -- in-order, in the split's
// own goroutine -- so any NEWLY STARTED call is guaranteed a cache miss and
// recomputes fresh from disk regardless of the locking bug. The actual bug is
// only observable in calls whose cache-check happens to straddle the
// cat.Put-to-invalidate window, which is why this test uses the
// "after_catalog_put_before_invalidate" test hook (see runAtomicCommitHook's
// doc comment) to reliably hold that window open long enough for the
// concurrent reader to land in it, rather than relying on natural goroutine
// scheduling luck to hit a window that is normally only nanoseconds wide.)
//
// With the fix (cs.stripes[stripeFor(originalFileID)] held across the whole
// sequence, including the hook), the reader simply blocks for the duration of
// the hook and can only resume once the lock is released after
// InvalidateHeaderCache has already run -- so it must always observe a cache
// miss and recompute fresh, post-split content. This test asserts exactly
// that: once the hook fires (i.e. from the earliest possible instant the race
// window could be entered), every ReadPartial(originalFileID) result the
// reader goroutine observes, for the remainder of the test, must be the
// post-split (empty, redirect-stub) answer -- never the pre-split cached
// headers.
func TestSectionIndexInvalidationConcurrent(t *testing.T) {
	const oldPath = "old/topic.md"

	idAlloc, cs, cat, w := newTestContentStoreDepsWithWAL(t)
	tree := newTestBtree(t)
	appender := newTestEdgeAppenderTracked(t)

	originalFileID, err := idAlloc.Next()
	if err != nil {
		t.Fatalf("idAlloc.Next() (originalFileID): %v", err)
	}

	guard := NewFileGuard()
	if !guard.TryAcquire(originalFileID) {
		t.Fatalf("guard.TryAcquire: expected success")
	}

	part1 := []byte("# Part1 Header\nbody1\n")
	part2 := []byte("# Part2 Header\nbody2\n")
	originalContent := append(append([]byte{}, part1...), part2...)

	rec := catalog.CatalogRecord{
		FileID:    originalFileID,
		Status:    catalog.StatusSplitting,
		SizeBytes: uint64(len(originalContent)),
	}
	if _, err := cs.Create(rec, originalContent); err != nil {
		t.Fatalf("cs.Create(original): %v", err)
	}

	// Populate the pre-split cache entry the race needs: without this, there
	// is nothing stale for a racy ReadPartial call to return.
	preSplit, err := cs.ReadPartial(originalFileID)
	if err != nil {
		t.Fatalf("ReadPartial (pre-split, priming cache): %v", err)
	}
	wantPreSplit := []catalog.HeaderOffset{{Header: "# Part1 Header", Offset: 0}, {Header: "# Part2 Header", Offset: len(part1)}}
	if !reflect.DeepEqual(preSplit, wantPreSplit) {
		t.Fatalf("ReadPartial (pre-split, priming cache) = %+v, want %+v", preSplit, wantPreSplit)
	}

	plan := SplitPlan{
		Files: []SplitFileProposal{
			{NewPath: "new/part-1.md", SectionRanges: []SectionRange{{Start: 0, End: len(part1)}}},
			{NewPath: "new/part-2.md", SectionRanges: []SectionRange{{Start: len(part1), End: len(originalContent)}}},
		},
		RedirectSummary: "see new/part-1.md, new/part-2.md",
	}

	// Coordination: a start barrier so the reader goroutine is definitely
	// spinning before the split begins, and a hook that fires exactly inside
	// the race window (after cat.Put, before InvalidateHeaderCache) to widen
	// it long enough for the concurrent reader to reliably land inside it.
	var (
		readerStarted   sync.WaitGroup
		stopReader      atomic.Bool
		badReadDetected atomic.Bool
		badReadValue    atomic.Value // []catalog.HeaderOffset, set at most once
		hookFired       atomic.Bool
	)
	readerStarted.Add(1)

	var readerWG sync.WaitGroup
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		first := true
		for !stopReader.Load() {
			got, err := cs.ReadPartial(originalFileID)
			if err != nil {
				// A transient ErrNotFound-style error is not what this test is
				// checking for; only content correctness matters here.
				if first {
					readerStarted.Done()
					first = false
				}
				continue
			}
			if first {
				readerStarted.Done()
				first = false
			}
			// Once the race-window hook has fired at least once, every
			// ReadPartial result the reader observes from that point on must be
			// the post-split (empty) answer -- a non-empty result here means a
			// racy call returned the pre-split cache entry despite the hook
			// (and, in production, the real cat.Put-to-invalidate window)
			// already having been entered.
			if hookFired.Load() && len(got) != 0 {
				if badReadDetected.CompareAndSwap(false, true) {
					badReadValue.Store(got)
				}
			}
		}
	}()
	readerStarted.Wait()

	setAtomicCommitHook(t, func(stage string) error {
		if stage == "after_catalog_put_before_invalidate" {
			hookFired.Store(true)
			// Hold the window open briefly so the concurrent reader gets many
			// chances to land in it. With the Bug 1 fix in place, the reader is
			// blocked on cs.stripes for this entire sleep (cannot even enter its
			// critical section), so this sleep is "free" -- it does not by
			// itself cause flakiness either way.
			time.Sleep(5 * time.Millisecond)
		}
		return nil
	})

	updated, err := ExecuteSplitAtomic(idAlloc, cat, cs, tree, appender, w, guard, oldPath, originalFileID, originalContent, plan)
	if err != nil {
		t.Fatalf("ExecuteSplitAtomic: %v", err)
	}
	if len(updated.RedirectTargetIDs) != 2 {
		t.Fatalf("updated.RedirectTargetIDs = %v, want 2 entries", updated.RedirectTargetIDs)
	}

	// Let the reader keep spinning a little longer after the split has
	// returned, to also exercise the "subsequent calls after the split call
	// returns" half of the acceptance criteria, then stop it.
	time.Sleep(5 * time.Millisecond)
	stopReader.Store(true)
	readerWG.Wait()

	if !hookFired.Load() {
		t.Fatal("race-window hook never fired; test did not actually exercise the cat.Put-to-invalidate window")
	}
	if badReadDetected.Load() {
		t.Fatalf("concurrent ReadPartial(originalFileID) returned stale pre-split header offsets during/after the split: %+v (want empty, post-split redirect-stub content) -- Bug 1 (missing cs.stripes lock across split's content-write+cat.Put+invalidate) reproduced", badReadValue.Load())
	}

	// Final sanity check matching TestSectionIndexInvalidation: after the
	// split has fully returned and the reader has stopped, a fresh
	// ReadPartial call must see the post-split state.
	finalRead, err := cs.ReadPartial(originalFileID)
	if err != nil {
		t.Fatalf("ReadPartial (final, post-split): %v", err)
	}
	if len(finalRead) != 0 {
		t.Fatalf("ReadPartial (final, post-split) = %+v, want empty", finalRead)
	}
}
