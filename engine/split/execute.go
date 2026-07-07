package split

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
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

// redirectStubHeader is the fixed first line of every redirect-stub content
// file this package writes (see buildRedirectStubContent). It is a simple
// human/debug-readable marker only -- no consumer in this issue's later
// subtasks (2b.3.3's B+Tree repoint, 2b.3.4/2b.3.5's graph edges) needs to
// parse stub content; they operate off CatalogRecord.RedirectTargetIDs
// directly, which remains the authoritative source of truth for where a
// split-off file's content now lives. See
// .cdr/runs/2026-07-07/017-implementation/architecture-discovery.md for the
// full reasoning behind this deliberately minimal format.
const redirectStubHeader = "HIVEMIND-REDIRECT-STUB v1"

// ErrNotSplit is returned by ExecuteSplitRedirectStub when the original
// file's catalog record's Status is not catalog.StatusSplit at the moment
// the redirect-stub transition is attempted -- e.g. called before
// Orchestrator.EndSplit(fileID, catalog.StatusSplit) has run for this
// fileID, or called twice for the same split. Mirrors
// orchestrate.go's ErrNotSplitting/ErrAlreadySplitting refusal-not-repair
// posture: never silently proceeds over an unexpected Status.
var ErrNotSplit = errors.New("split: execute: redirect stub: catalog record is not StatusSplit")

// buildRedirectStubContent deterministically renders the redirect-stub
// content file's bytes for targetFileIDs: a fixed header line followed by
// one decimal fileID per line, in the given order (the same order as the
// RedirectTargetIDs slice written to the catalog record), so tests and any
// future debugging can assert against it byte-for-byte.
func buildRedirectStubContent(targetFileIDs []uint64) []byte {
	var b strings.Builder
	b.WriteString(redirectStubHeader)
	b.WriteByte('\n')
	for _, id := range targetFileIDs {
		b.WriteString(strconv.FormatUint(id, 10))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// ExecuteSplitRedirectStub is subtask 2b.3.2's ("Write redirect/stub at old
// path + update catalog entries") execution primitive. It consumes
// newFileIDs -- typically the values of the map ExecuteSplitAllocateAndWrite
// (2b.3.1) returned -- and performs the NEXT step for the ORIGINAL file
// identified by originalFileID: it overwrites that fileID's content file
// with a redirect stub (see buildRedirectStubContent), then durably
// transitions its catalog record's Status from catalog.StatusSplit to
// catalog.StatusRedirect with RedirectTargetIDs set to newFileIDs.
//
// This is deliberately the SECOND half of a two-step status transition:
// step one, catalog.StatusActive/StatusSplitting -> catalog.StatusSplit, is
// 2b.1.3's Orchestrator.EndSplit(fileID, catalog.StatusSplit)'s job and is
// expected to have already run for originalFileID before this function is
// called (ExecuteSplitRedirectStub refuses with ErrNotSplit, mutating
// nothing, if the record's current Status is not catalog.StatusSplit).
// Step two, catalog.StatusSplit -> catalog.StatusRedirect, is this
// function's job.
//
// The original fileID is reused for the stub -- no new fileID is allocated
// for the old path. cs.ContentPath(originalFileID) is overwritten in place,
// and CatalogRecord.FileID is unchanged; only Status, RedirectTargetIDs, and
// SizeBytes are updated on the record (LastModified is left untouched,
// matching the fact that no existing call site in this repo -- Create,
// Append, or Orchestrator's transitionStatus -- populates it yet either).
// See
// architecture-discovery.md for the full reasoning (fileID-reuse decision,
// stub-format decision, and the ordering/idempotency risk this leaves for
// 2b.3.6 to wrap in a single atomic WAL-covered transaction).
//
// Ordering: the stub content file is written BEFORE the catalog Status
// transition is durably applied, matching this repo's "catalog record is
// what makes state visible" convention (engine/catalog/content.go,
// split/orchestrate.go). A crash between the two leaves the record at
// StatusSplit with the old path's physical content already stub-shaped but
// RedirectTargetIDs/Status not yet updated -- a non-atomic intermediate
// state that 2b.3.6's WAL-covered transaction is explicitly responsible for
// eliminating; see architecture-discovery.md's "Ordering / idempotency
// risk" section.
//
// Scope boundary: this function never touches the B+Tree (2b.3.3), graph
// edges (2b.3.4/2b.3.5), or adds any WAL/fsync transactional wrapping
// spanning more than this function's own single catalog Put (2b.3.6).
func ExecuteSplitRedirectStub(
	cat *catalog.Catalog,
	w *wal.Writer,
	cs *catalog.ContentStore,
	originalFileID uint64,
	newFileIDs []uint64,
) (catalog.CatalogRecord, error) {
	if cat == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: cat must not be nil")
	}
	if w == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: w must not be nil")
	}
	if cs == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: cs must not be nil")
	}
	if len(newFileIDs) == 0 {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: newFileIDs must not be empty")
	}
	if len(newFileIDs) > catalog.MaxRedirectTargets {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: got %d redirect targets, max %d", len(newFileIDs), catalog.MaxRedirectTargets)
	}

	rec, err := cat.Get(originalFileID)
	if err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: reading fileID %d: %w", originalFileID, err)
	}
	if rec.Status != catalog.StatusSplit {
		return catalog.CatalogRecord{}, fmt.Errorf("%w: fileID %d has Status %v", ErrNotSplit, originalFileID, rec.Status)
	}

	targets := make([]uint64, len(newFileIDs))
	copy(targets, newFileIDs)

	stubContent := buildRedirectStubContent(targets)
	if err := writeNewContentFile(cs.ContentPath(originalFileID), stubContent); err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: writing stub content for fileID %d: %w", originalFileID, err)
	}

	updated := rec
	updated.Status = catalog.StatusRedirect
	updated.RedirectTargetIDs = targets
	updated.SizeBytes = uint64(len(stubContent))

	encoded, err := updated.Encode()
	if err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: encoding fileID %d: %w", originalFileID, err)
	}

	walRec := wal.NewCatalogPutRecord(originalFileID, encoded)
	if _, err := wal.AppendAndApply(w, walRec, func() error {
		if err := cat.Put(updated); err != nil {
			return fmt.Errorf("committing catalog record fileID %d: %w", originalFileID, err)
		}
		return nil
	}); err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: %w", err)
	}

	return updated, nil
}
