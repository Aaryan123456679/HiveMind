package split

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
)

// ExecuteSplitAllocateAndWrite is subtask 2b.3.1's ("Allocate new fileIDs + write
// new .md files split-off content per split plan") execution primitive. For each
// SplitFileProposal in plan.Files, it allocates one new fileID (via idAlloc.Next(),
// this repo's established monotonic-allocator convention -- see
// engine/catalog/idalloc.go's IDAllocator) and durably writes a new content file
// (via cs.ContentPath(newFileID), reusing the same "content/<fileID>.v1.md" path
// convention engine/catalog/content.go's ContentStore uses) containing exactly the
// bytes described by that proposal's SectionRanges, sliced from originalContent and
// concatenated in order.
//
// Scope boundary (see .cdr/runs/2026-07-07/014-implementation/architecture-discovery.md
// for the full reasoning): this function is deliberately narrow. It never touches
// cat -- no catalog.CatalogRecord is created, no catalog.Catalog.Put is called, and
// no engine/wal record is appended -- because catalog visibility (status
// REDIRECT/SPLIT, RedirectTargetIDs) is issue #12's subtask 2b.3.2's job, B+Tree
// repointing is 2b.3.3's, graph edges are 2b.3.4/2b.3.5's, and wrapping all of the
// above in one atomic WAL-covered transaction is 2b.3.6's. idAlloc.Next() itself is
// already individually crash-durable (it fsyncs its own high-water-mark before
// returning), and each content file is written with a temp-file+rename technique
// (see writeNewContentFile below) mirroring engine/catalog/content.go's
// writeContentFile, so a crash mid-write can never leave a torn/partial file
// visible at its final path -- but no cross-file/cross-step atomicity is provided
// here; that composition is explicitly deferred to 2b.3.6.
//
// Validation happens entirely before any allocation or write is performed, so a
// rejected plan never partially allocates fileIDs or partially writes files:
//   - plan.Files must be non-empty.
//   - every SplitFileProposal.NewPath must be non-empty and unique within plan.
//   - every SplitFileProposal must have at least one SectionRange.
//   - every SectionRange must satisfy 0 <= Start <= End <= len(originalContent).
//   - no two SectionRanges anywhere in the plan (whether in the same proposal or
//     different ones) may overlap; zero-length ranges (Start == End) never overlap
//     anything.
//
// On success, ExecuteSplitAllocateAndWrite returns a map from each proposal's
// NewPath to its newly allocated fileID, so a later subtask (2b.3.2) can wire up
// catalog records for those fileIDs without re-deriving them.
func ExecuteSplitAllocateAndWrite(
	idAlloc *catalog.IDAllocator,
	cs *catalog.ContentStore,
	originalContent []byte,
	plan SplitPlan,
) (map[string]uint64, error) {
	if idAlloc == nil {
		return nil, fmt.Errorf("split: execute: idAlloc must not be nil")
	}
	if cs == nil {
		return nil, fmt.Errorf("split: execute: cs must not be nil")
	}

	if err := validateSplitPlan(plan, len(originalContent)); err != nil {
		return nil, fmt.Errorf("split: execute: invalid split plan: %w", err)
	}

	result := make(map[string]uint64, len(plan.Files))
	for _, proposal := range plan.Files {
		newFileID, err := idAlloc.Next()
		if err != nil {
			return nil, fmt.Errorf("split: execute: allocating fileID for %q: %w", proposal.NewPath, err)
		}

		content := extractSections(originalContent, proposal.SectionRanges)

		if err := writeNewContentFile(cs.ContentPath(newFileID), content); err != nil {
			return nil, fmt.Errorf("split: execute: writing content file for %q (fileID %d): %w", proposal.NewPath, newFileID, err)
		}

		result[proposal.NewPath] = newFileID
	}

	return result, nil
}

// validateSplitPlan checks plan against originalContentLen (the length, in bytes,
// of the original file's content) before ExecuteSplitAllocateAndWrite allocates or
// writes anything. See ExecuteSplitAllocateAndWrite's doc comment for the exact
// rules enforced.
func validateSplitPlan(plan SplitPlan, originalContentLen int) error {
	if len(plan.Files) == 0 {
		return fmt.Errorf("split plan has no files")
	}

	type interval struct {
		start, end int
		newPath    string
	}
	var all []interval

	seenPaths := make(map[string]bool, len(plan.Files))
	for _, proposal := range plan.Files {
		if proposal.NewPath == "" {
			return fmt.Errorf("split plan contains a proposal with an empty NewPath")
		}
		if seenPaths[proposal.NewPath] {
			return fmt.Errorf("split plan contains duplicate NewPath %q", proposal.NewPath)
		}
		seenPaths[proposal.NewPath] = true

		if len(proposal.SectionRanges) == 0 {
			return fmt.Errorf("proposal %q has no section ranges", proposal.NewPath)
		}

		for _, r := range proposal.SectionRanges {
			if r.Start < 0 || r.End < r.Start || r.End > originalContentLen {
				return fmt.Errorf("proposal %q has out-of-bounds or inverted section range [%d, %d) against content of length %d", proposal.NewPath, r.Start, r.End, originalContentLen)
			}
			all = append(all, interval{start: r.Start, end: r.End, newPath: proposal.NewPath})
		}
	}

	// Overlap check: sort all ranges (across every proposal) by start offset, then
	// scan adjacent pairs. Zero-length ranges (start == end) never overlap
	// anything, including themselves, by construction of this comparison.
	sort.Slice(all, func(i, j int) bool { return all[i].start < all[j].start })
	for i := 1; i < len(all); i++ {
		prev, cur := all[i-1], all[i]
		if prev.end > cur.start && prev.end > prev.start && cur.end > cur.start {
			return fmt.Errorf("section ranges overlap: %q's [%d, %d) overlaps %q's [%d, %d)", prev.newPath, prev.start, prev.end, cur.newPath, cur.start, cur.end)
		}
	}

	return nil
}

// extractSections concatenates originalContent[r.Start:r.End] for each r in
// ranges, in order, returning the assembled bytes for one new file. Callers must
// have already validated ranges via validateSplitPlan; extractSections itself does
// no bounds checking.
func extractSections(originalContent []byte, ranges []SectionRange) []byte {
	total := 0
	for _, r := range ranges {
		total += r.End - r.Start
	}

	out := make([]byte, 0, total)
	for _, r := range ranges {
		out = append(out, originalContent[r.Start:r.End]...)
	}
	return out
}

// writeNewContentFile durably writes data to finalPath, mirroring
// engine/catalog/content.go's ContentStore.writeContentFile technique exactly
// (temp file in the same directory -> Write -> Sync -> Rename), so a crash
// mid-write can never leave a torn/partial file visible at finalPath. It is a
// standalone helper (rather than a call into catalog.ContentStore's unexported
// writeContentFile) because this subtask must not perform ContentStore.Create's or
// Append's accompanying catalog mutation -- see ExecuteSplitAllocateAndWrite's doc
// comment on why catalog visibility is deferred to 2b.3.2.
func writeNewContentFile(finalPath string, data []byte) error {
	dir := filepath.Dir(finalPath)

	tmp, err := os.CreateTemp(dir, filepath.Base(finalPath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp content file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing temp content file %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("syncing temp content file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp content file %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, finalPath, err)
	}

	return nil
}
