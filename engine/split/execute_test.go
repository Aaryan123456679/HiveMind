package split

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
