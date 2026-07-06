package split

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
)

// newTestContentStoreDeps opens a fresh catalog.FileManager-backed
// catalog.IDAllocator and catalog.ContentStore, rooted at an isolated
// t.TempDir(), reusing the same newTestCatalog/newTestWAL helpers
// orchestrate_test.go already defines in this package.
func newTestContentStoreDeps(t *testing.T) (*catalog.IDAllocator, *catalog.ContentStore, *catalog.Catalog) {
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

	return idAlloc, cs, cat
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
