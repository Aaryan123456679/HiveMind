package split

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Aaryan123456679/HiveMind/engine/btree"
	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/graph"
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

	// Acquire the SAME per-fileID stripe lock ContentStore.Append's own
	// read-modify-write critical section takes (via the exported
	// LockFileContent method, since ContentStore.stripes itself stays
	// unexported outside engine/catalog) across this stub write, the catalog
	// Put that makes it visible, and the header-cache invalidation that
	// follows. Fixes issue #13's CHANGES_REQUESTED verification finding: prior
	// to this lock, a concurrent ReadPartial(originalFileID) could interleave
	// between the durable cat.Put below and InvalidateHeaderCache and cache a
	// soon-to-be-stale header index. See LockFileContent's doc comment for the
	// full lock-ordering reasoning (cs.stripes -> wal.Writer-internal ->
	// cat.stripes, the exact nesting order Append already establishes).
	unlock := cs.LockFileContent(originalFileID)
	defer unlock()

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

		// Invalidate originalFileID's cached header-offset index (see issue #13's
		// subtask 2b.4.1): this stub rewrite just changed its content, so any
		// cache entry computed against the pre-stub content must not survive
		// past this commit. Called from inside the apply closure so eviction
		// only takes effect once this transaction has actually committed, and
		// still under the cs.stripes lock acquired above, closing the race the
		// fix cycle addresses.
		cs.InvalidateHeaderCache(originalFileID)

		return nil
	}); err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: redirect stub: %w", err)
	}

	return updated, nil
}

// normalizeTopicPath is subtask 4.5.3.4's ("Add topic-path key
// normalization/namespace layer for B+Tree keys used by split execution")
// canonicalization layer. It is applied to every topic-path string
// (oldPath and every newPath) immediately before that string is used as a
// *btree.Tree key by ExecuteSplitBtreeInsert, ExecuteSplitAtomic's apply
// closure, and RecoverSplitCommits's replay loop -- the three call sites in
// this file that turn a topic path into a raw B+Tree key (see
// .cdr/memory/pending.md's "Raw topic-path strings used directly as B+Tree
// keys, no normalization/namespace layer" entry, forwarded from task-2b.3.3 /
// issue #12).
//
// Rules applied, in order:
//  1. Backslashes are converted to forward slashes, so a caller that types a
//     Windows-style separator normalizes to the same key as one that types a
//     forward slash.
//  2. A leading "./" is stripped (repeatedly, in case of "././a").
//  3. Runs of consecutive slashes are collapsed to a single slash ("a//b" ->
//     "a/b").
//  4. Trailing slashes are stripped ("a/b/" -> "a/b"), directly covering the
//     test spec's explicit "trailing separators" example.
//
// normalizeTopicPath is deliberately narrow in scope: it does NOT case-fold
// (topic paths are treated as case-sensitive, matching the Markdown filename
// conventions used throughout this package's fixtures) and it does NOT
// resolve ".." segments or otherwise attempt general filesystem-path
// resolution -- this is a key-canonicalization layer for the B+Tree's opaque
// string-key space, not a filesystem path resolver. It is idempotent:
// normalizeTopicPath(normalizeTopicPath(p)) == normalizeTopicPath(p) for
// every input p, and a no-op for already-canonical paths (the common case,
// and the case every existing fixture in this package already uses).
//
// btree.Tree.Lookup itself is untouched by this subtask and does not
// normalize its argument -- callers that look up a topic path must either
// already know its canonical form (as every existing call site in this
// package does) or normalize it themselves via this same function before
// calling Lookup. Retrofitting normalization into engine/btree.Tree.Lookup
// itself is out of scope here (a different package, not named in this
// subtask's "Impacted modules").
func normalizeTopicPath(path string) string {
	normalized := strings.ReplaceAll(path, `\`, "/")

	for strings.HasPrefix(normalized, "./") {
		normalized = normalized[2:]
	}

	for strings.Contains(normalized, "//") {
		normalized = strings.ReplaceAll(normalized, "//", "/")
	}

	if normalized != "/" {
		normalized = strings.TrimRight(normalized, "/")
	}

	return normalized
}

// ExecuteSplitBtreeInsert is subtask 2b.3.3's ("Insert new topic paths into
// B+Tree; repoint old path's entry to redirect stub") execution primitive.
// It consumes newPathFileIDs -- typically the map ExecuteSplitAllocateAndWrite
// (2b.3.1) returned -- and, against the given *btree.Tree:
//
//  1. Inserts every (newPath, newFileID) pair from newPathFileIDs, so each new
//     topic path resolves via btree.Tree.Lookup to its own new fileID.
//  2. Repoints oldPath's B+Tree entry to originalFileID via an explicit
//     tree.Insert(oldPath, originalFileID) call.
//
// See .cdr/runs/2026-07-07/019-implementation/architecture-discovery.md for
// the full reasoning behind step 2. In short: btree.Tree.Insert (like the
// free btree.Insert function it wraps) has upsert semantics -- inserting an
// already-present key just updates its fileID in place, with no structural
// change and no split possible. Because 2b.3.2 (ExecuteSplitRedirectStub)
// REUSES originalFileID for the redirect-stub content (no new fileID is ever
// allocated for the old path), oldPath's key->fileID mapping is, in the
// strict sense, already correct with zero B+Tree mutation: oldPath already
// maps to originalFileID, and originalFileID's CONTENT is what 2b.3.2 changed
// (to the redirect stub), not its identity. The explicit repoint call here is
// therefore a guaranteed-safe, single-field-write no-op when that invariant
// already holds -- but it is included anyway so that this function is
// self-contained and independently correct (idempotent insert-or-update)
// rather than silently depending on some earlier, unrelated call having
// already indexed oldPath in this tree. That matters because, as of this
// subtask, no other code path in this repo populates a *btree.Tree for a
// topic path at all (grepped the whole repo; the only path/fileID indexing
// convention that exists anywhere today is btree.Tree's own API).
//
// Scope boundary: this function never touches engine/graph/ edges
// (2b.3.4/2b.3.5's job) and adds no WAL/fsync transactional wrapping beyond
// what btree.Tree's own node writes already durably provide individually
// (cross-step atomicity spanning 2b.3.1/2b.3.2/2b.3.3/graph writes is
// 2b.3.6's job).
func ExecuteSplitBtreeInsert(
	tree *btree.Tree,
	oldPath string,
	originalFileID uint64,
	newPathFileIDs map[string]uint64,
) error {
	if tree == nil {
		return fmt.Errorf("split: execute: btree insert: tree must not be nil")
	}
	if oldPath == "" {
		return fmt.Errorf("split: execute: btree insert: oldPath must not be empty")
	}
	if len(newPathFileIDs) == 0 {
		return fmt.Errorf("split: execute: btree insert: newPathFileIDs must not be empty")
	}

	// Iterate in a deterministic, sorted order rather than Go's randomized
	// map iteration order, so any error returned (and the set of paths
	// successfully inserted before a failure) is reproducible across runs.
	newPaths := make([]string, 0, len(newPathFileIDs))
	for newPath := range newPathFileIDs {
		newPaths = append(newPaths, newPath)
	}
	sort.Strings(newPaths)

	normalizedOldPath := normalizeTopicPath(oldPath)

	for _, newPath := range newPaths {
		if newPath == "" {
			return fmt.Errorf("split: execute: btree insert: newPathFileIDs contains an empty path")
		}
		normalizedNewPath := normalizeTopicPath(newPath)
		if normalizedNewPath == normalizedOldPath {
			return fmt.Errorf("split: execute: btree insert: new path %q must not equal oldPath", newPath)
		}

		if err := tree.Insert(normalizedNewPath, newPathFileIDs[newPath]); err != nil {
			return fmt.Errorf("split: execute: btree insert: inserting new path %q (normalized %q, fileID %d): %w", newPath, normalizedNewPath, newPathFileIDs[newPath], err)
		}
	}

	// Repoint the old path's entry. See doc comment above: this is a
	// guaranteed upsert no-op when oldPath already maps to originalFileID
	// (the common case, since 2b.3.2 reuses the fileID), and idempotently
	// establishes that mapping otherwise.
	if err := tree.Insert(normalizedOldPath, originalFileID); err != nil {
		return fmt.Errorf("split: execute: btree insert: repointing old path %q (normalized %q, fileID %d): %w", oldPath, normalizedOldPath, originalFileID, err)
	}

	return nil
}

// ExecuteSplitGraphEdges is subtask 2b.3.5's ("Add SPLIT_SIBLING edges
// between new files; repoint inbound edges to redirect stub") execution
// primitive. It consumes newFileIDs -- typically the values of the map
// ExecuteSplitAllocateAndWrite (2b.3.1) returned -- and, via appender
// (an engine/graph.EdgeAppender rooted at this split's edge log):
//
//  1. Appends a graph.EdgeSplitSibling edge for every ORDERED pair of
//     distinct new fileIDs (i.e. a complete directed graph over the new
//     fileIDs: N*(N-1) edges for N new files). Both directions are appended
//     for every unordered pair, because "sibling" is a symmetric
//     relationship with no natural direction, and this lets a future
//     traversal reader discover siblings starting from any one of them via
//     a plain Source-equality filter, without needing undirected-edge-aware
//     traversal logic. See architecture-discovery.md
//     (.cdr/runs/2026-07-07/023-implementation/) for why an all-pairs
//     complete graph was chosen over a star-from-first-file topology (the
//     latter would silently depend on newFileIDs's ordering to pick a
//     "hub", which this function deliberately avoids).
//  2. Appends a graph.EdgeRedirect edge from originalFileID to EACH new
//     fileID, so a reader who lands on originalFileID (the redirect stub
//     left behind at the old path by 2b.3.2, which REUSES originalFileID --
//     no new fileID is ever allocated for the old path) can discover where
//     the content actually moved to.
//
// On "repoint inbound edges to redirect stub": because 2b.3.2 reuses
// originalFileID for the stub, ANY pre-existing graph edge whose Target is
// originalFileID ALREADY points at the stub, with zero graph mutation
// required -- there is nothing to rewrite, and nothing this function needs
// to append to achieve that (matching engine/graph's append-only design,
// which offers no edge-mutation API in the first place; see
// architecture-discovery.md's part (a)). This function only appends the new
// EdgeRedirect edges described in step 2 above, which is the other half of
// the redirect relationship: how a reader discovers the new fileIDs once
// they've landed on the (unchanged-identity) stub.
//
// Crash-recovery scope boundary: like the other Execute* functions in this
// file, this function performs no WAL/fsync transactional wrapping beyond
// what appender.AppendEdge itself already durably provides for each
// individual call (see engine/graph/edge_append.go's doc comment). The
// broader gap -- that graph edge-append records are durable at the byte
// level but not yet integrated into any wal.Replay-based crash-recovery
// path the way catalog/btree records are (tracked in
// .cdr/memory/pending.md) -- is DELIBERATELY NOT resolved here: 2b.3.6's
// own acceptance criteria explicitly lists "graph edge writes" among what
// must commit atomically under one WAL-covered, fsynced transaction, so
// full resolution is left to 2b.3.6. See architecture-discovery.md's part
// (c) for the full reasoning.
func ExecuteSplitGraphEdges(
	appender *graph.EdgeAppender,
	originalFileID uint64,
	newFileIDs []uint64,
) error {
	if appender == nil {
		return fmt.Errorf("split: execute: graph edges: appender must not be nil")
	}
	if len(newFileIDs) == 0 {
		return fmt.Errorf("split: execute: graph edges: newFileIDs must not be empty")
	}

	// Sort a private copy so on-disk append order is deterministic,
	// independent of the caller's (possibly map-iteration-derived, thus
	// unordered) newFileIDs slice.
	ids := make([]uint64, len(newFileIDs))
	copy(ids, newFileIDs)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for i := range ids {
		for j := range ids {
			if i == j {
				continue
			}
			edge := graph.Edge{Source: ids[i], Target: ids[j], Type: graph.EdgeSplitSibling}
			if err := appender.AppendEdge(edge); err != nil {
				return fmt.Errorf("split: execute: graph edges: appending SPLIT_SIBLING edge %d->%d: %w", ids[i], ids[j], err)
			}
		}
	}

	for _, id := range ids {
		edge := graph.Edge{Source: originalFileID, Target: id, Type: graph.EdgeRedirect}
		if err := appender.AppendEdge(edge); err != nil {
			return fmt.Errorf("split: execute: graph edges: appending REDIRECT edge %d->%d: %w", originalFileID, id, err)
		}
	}

	return nil
}

// atomicCommitHook, if non-nil, is invoked synchronously by ExecuteSplitAtomic
// at well-defined stages of its atomic commit sequence, letting tests
// deterministically simulate a crash at exactly that point: a stage callback
// that returns a non-nil error causes ExecuteSplitAtomic to propagate that
// error immediately, executing no further steps -- indistinguishable, from
// the caller's perspective, from the process having actually died at that
// exact instant (no deferred/finalizer/graceful-shutdown code runs that a
// real crash wouldn't also skip). nil (a no-op) in production. This mirrors
// this repo's established test-only synchronous-hook idiom (see e.g.
// engine/btree/lookup.go's optimisticReadHook/optimisticRetryHook, and
// engine/btree/insert.go's crabRetryHook).
//
// Recognized stage names (see ExecuteSplitAtomic's doc comment for the full
// failure-model writeup this test-only injection point is designed around):
//   - "before_commit_append": immediately before the split's single WAL
//     commit record (wal.RecordSplitCommit) is appended -- i.e. before the
//     transaction's point of no return. A crash injected here must leave NO
//     visible catalog/B+Tree/graph effect: only harmless, unreferenced
//     orphan content files (the new split-off files and the not-yet-visible
//     redirect stub) may already be on disk.
//   - "after_commit_before_catalog_put": immediately after the WAL commit
//     record has been durably fsynced (wal.AppendAndApply's Append call has
//     already returned successfully), before cat.Put is even invoked. A
//     crash injected here must be fully, deterministically recoverable via
//     RecoverSplitCommits.
//   - "after_catalog_put_before_invalidate": after cat.Put has applied the
//     final (StatusRedirect) catalog record, before InvalidateHeaderCache
//     evicts the pre-split header-offset cache entry (issue #13's 2b.4.1 fix
//     cycle). Not a crash/recovery checkpoint (the header cache is
//     in-memory-only, so a crash here has no recovery implications --
//     RecoverSplitCommits does not touch it); purely a concurrency-test seam
//     for TestSectionIndexInvalidationConcurrent to hold this window open and
//     confirm cs.stripes[stripeFor(originalFileID)] (acquired above, around
//     this whole apply step) genuinely excludes concurrent ReadPartial calls
//     from it.
//   - "after_catalog_put_before_btree": after cat.Put has applied the final
//     (StatusRedirect) catalog record and the header cache has been
//     invalidated, before the B+Tree inserts run. Also must be fully
//     recoverable.
//   - "after_btree_before_graph": after the B+Tree inserts (new paths, plus
//     the old path's repoint to the reused originalFileID) have run, before
//     the graph edge appends run. Also must be fully recoverable.
var atomicCommitHook func(stage string) error

// runAtomicCommitHook invokes atomicCommitHook for stage if it is set,
// returning its error (wrapped with stage context) if any, or nil if the
// hook is unset or returns nil. Centralizing this here (rather than
// repeating the nil-check at every call site) keeps ExecuteSplitAtomic's own
// body focused on its real steps.
func runAtomicCommitHook(stage string) error {
	if atomicCommitHook == nil {
		return nil
	}
	if err := atomicCommitHook(stage); err != nil {
		return fmt.Errorf("split: execute: atomic commit: simulated crash at stage %q: %w", stage, err)
	}
	return nil
}

// ExecuteSplitAtomic is subtask 2b.3.6's ("Commit entire split as a single
// WAL-covered, fsynced transaction; release queued writers on commit")
// execution primitive: the capstone that composes 2b.3.1
// (ExecuteSplitAllocateAndWrite), 2b.3.2's catalog-transition half
// (ExecuteSplitRedirectStub's Split->Redirect logic, inlined here rather than
// called directly -- see "Why not just call the four prior Execute* functions
// in sequence" below), 2b.3.3 (ExecuteSplitBtreeInsert's logic), and 2b.3.5
// (ExecuteSplitGraphEdges's logic, via the shared appendSplitGraphEdges
// helper) under ONE atomic, crash-safe transaction discipline.
//
// # Precondition
//
// originalFileID's catalog record must already have Status ==
// catalog.StatusSplitting (i.e. a prior, successful
// Orchestrator.BeginSplit(originalFileID) call, per 2b.1.3) -- ExecuteSplitAtomic
// refuses with ErrNotSplitting, mutating nothing, otherwise. Deliberately
// StatusSplitting, not catalog.StatusSplit (contrast ExecuteSplitRedirectStub's
// own precondition): see "release queued writers on commit" below for why.
//
// # Failure model and the atomicity guarantee actually achieved
//
// A crash (or any error) can occur at one of a small number of well-defined
// points, each independently exercised by TestSplitAtomicCommit via
// atomicCommitHook:
//
//  1. Before any content files are written, or after new-file/stub content
//     files are written but before the WAL commit record is appended
//     ("before_commit_append"): NOTHING durable and catalog/B+Tree/graph
//     -visible has happened. The new-file and stub content files may already
//     exist on disk, but they are unreferenced by any catalog record, B+Tree
//     entry, or graph edge -- inert, harmless garbage, not a partial split.
//     The old path continues to resolve exactly as it did before this call
//     (Status remains StatusSplitting; a caller may retry the whole split,
//     which will allocate fresh fileIDs and leave the previous attempt's
//     orphan files behind, exactly as 2b.3.1's own documented scope boundary
//     already discloses). This satisfies "no partial split, pre-split state
//     fully intact."
//
//  2. After the WAL commit record (wal.RecordSplitCommit) is durably
//     appended and fsynced -- Writer.Append inside wal.AppendAndApply has
//     returned successfully -- but before, or partway through, applying its
//     effects (cat.Put, the B+Tree inserts, the graph edge appends): the
//     split's FULL intended effect is now durably described on disk, even
//     though it may not yet be (fully) applied in memory/on-disk index
//     structures. This is the transaction's single point of no return. A
//     crash anywhere in this window is recovered by calling
//     RecoverSplitCommits against the same WAL directory, which decodes the
//     commit record and re-applies cat.Put, every B+Tree insert, and every
//     graph edge append (idempotently, via graph.EdgeAppender.AppendEdgeIfAbsent)
//     -- deterministically reaching the exact same fully-applied state
//     regardless of exactly how much of the original apply had already run
//     before the crash. This satisfies "full effect present after recovery,
//     or reliably replayable to become fully present."
//
// Only ONE fsync (the WAL commit record's) defines the boundary between
// these two cases; every step after it is designed to be safely re-run any
// number of times (cat.Put is a documented upsert; *btree.Tree.Insert is a
// documented upsert; AppendEdgeIfAbsent is check-then-append specifically to
// make graph edge replay idempotent too).
//
// # What is honestly NOT covered by this guarantee (residual risk)
//
//   - engine/btree's own persistence model (NodeStore's direct, per-call
//     durability plus a separate, manual, out-of-band SaveRoot checkpoint;
//     see .cdr/memory/pending.md's "btree SaveRoot / WAL-replay gap" item,
//     pre-existing since task-1.2, NOT introduced or fixed by this subtask)
//     is unchanged by RecoverSplitCommits: RecoverSplitCommits replays
//     directly against a live *btree.Tree object that the CALLER must have
//     already correctly reconstructed (i.e. pointed at the right root) by
//     whatever means engine/btree's own recovery story eventually provides.
//     This subtask closes the crash-recovery gap for WHICH mutations a
//     completed split needs redone (catalog + B+Tree + graph, all now
//     described by one durable record), not the separate, pre-existing
//     question of how the B+Tree's own root pointer survives a real process
//     restart. This is an explicit, bounded scope decision: fixing btree's
//     SaveRoot gap is a larger, separate concern that predates issue #12.
//   - FileGuard's in-memory splitInProgress flag does not survive a real
//     process restart (documented already in guard.go/orchestrate.go). A
//     crash before this fileID's guard is released via this function leaves
//     it logically "still splitting" from a fresh process's point of view
//     only if a fresh FileGuard is reused across the restart; in the much
//     more common case of a genuine process restart, a brand-new (empty)
//     FileGuard is constructed, and BeginSplit would need the CATALOG
//     record's Status (not the guard) to decide whether a fresh split attempt
//     is safe -- which RecoverSplitCommits makes correct, since it restores
//     Status to StatusRedirect (no longer StatusSplitting) once replayed.
//     The general "abandoned StatusSplitting record with no split ever
//     attempted at all" case (tracked in pending.md) is narrowed, but not
//     eliminated, by this subtask: specifically, it is narrowed down to
//     "crash occurred before THIS function's own WAL commit point", a much
//     smaller window than "any time between BeginSplit and completion." A
//     general lease/heartbeat/timeout-based reversion of a genuinely stuck
//     StatusSplitting record (with no split executor having ever reached its
//     own commit point) remains explicitly out of scope for this subtask, as
//     it was for 2b.1.3.
//
// # "Release queued writers on commit"
//
// Orchestrator.AdmitWrite (2b.1.3) already refuses writers with
// ErrSplitInProgress precisely when, and only when, a fileID's catalog
// record Status == catalog.StatusSplitting. That existing mechanism IS the
// "queued writers" gate; no new queue/channel/condvar primitive is added or
// needed here (2b.1.3's Orchestrator doc comment already anticipates this:
// "superseded once issue #12's single atomic WAL-covered commit lands, which
// is what actually releases queued writers on commit"). What THIS subtask
// resolves is exactly WHEN Status stops being StatusSplitting: it happens
// atomically, as part of this function's single WAL-covered apply step (the
// cat.Put call inside wal.AppendAndApply's apply closure), together with the
// B+Tree and graph updates -- not one WAL-covered step earlier and
// separately, the way calling Orchestrator.EndSplit(fileID, catalog.StatusSplit)
// before the redirect-stub/B+Tree/graph work (as 2b.3.2's own doc comments,
// written before this subtask existed, describe) would do. That earlier
// design would flip Status away from StatusSplitting -- and thus let
// AdmitWrite start admitting writers again -- BEFORE the redirect stub,
// B+Tree repoint, and graph edges were actually in place: exactly the
// "released before commit" bug this subtask's acceptance criteria warn
// against. ExecuteSplitAtomic therefore does NOT call Orchestrator.EndSplit
// at all; it performs its own single Splitting->Redirect catalog transition,
// batched into the same atomic apply as the B+Tree/graph updates, so
// Status leaves StatusSplitting at the exact same instant the rest of the
// split's effect becomes visible.
//
// Separately, guard.Release(originalFileID) (the FileGuard CAS flag
// TryAcquire originally won) is called only once the ENTIRE atomic commit
// has fully applied (all of cat.Put, every B+Tree insert, and every graph
// edge append succeeded) -- matching FileGuard's own documented "release
// once the split completes" contract, and ensuring a fresh split attempt for
// this fileID (or whatever it becomes) cannot be admitted mid-transaction.
//
// # Scope boundary
//
// ExecuteSplitAtomic does not call Orchestrator.BeginSplit itself (the
// caller is expected to have already done so); it also does not call
// ExecuteSplitRedirectStub or ExecuteSplitGraphEdges directly, since both of
// those functions perform their OWN independent WAL-covered or unwrapped
// mutation outside this function's single commit record -- reusing them
// as-is here would reintroduce exactly the multiple-separately-durable-steps
// problem this subtask exists to eliminate. Their stub-content-building
// (buildRedirectStubContent) and graph-edge-set (via the shared
// appendSplitGraphEdges helper) LOGIC is reused; their own WAL-transaction
// wrapping is not.
func ExecuteSplitAtomic(
	idAlloc *catalog.IDAllocator,
	cat *catalog.Catalog,
	cs *catalog.ContentStore,
	tree *btree.Tree,
	appender *graph.EdgeAppender,
	w *wal.Writer,
	guard *FileGuard,
	oldPath string,
	originalFileID uint64,
	originalContent []byte,
	plan SplitPlan,
) (catalog.CatalogRecord, error) {
	if idAlloc == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: idAlloc must not be nil")
	}
	if cat == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: cat must not be nil")
	}
	if cs == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: cs must not be nil")
	}
	if tree == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: tree must not be nil")
	}
	if appender == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: appender must not be nil")
	}
	if w == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: w must not be nil")
	}
	if guard == nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: guard must not be nil")
	}
	if oldPath == "" {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: oldPath must not be empty")
	}

	rec, err := cat.Get(originalFileID)
	if err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: reading fileID %d: %w", originalFileID, err)
	}
	if rec.Status != catalog.StatusSplitting {
		return catalog.CatalogRecord{}, fmt.Errorf("%w: fileID %d has Status %v", ErrNotSplitting, originalFileID, rec.Status)
	}

	// Allocate + write new split-off content files (2b.3.1's logic). Not yet
	// visible via catalog/B+Tree/graph: harmless if this is as far as we get
	// before a crash.
	newFileIDsByPath, err := ExecuteSplitAllocateAndWrite(idAlloc, cs, originalContent, plan)
	if err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: %w", err)
	}

	if len(newFileIDsByPath) == 0 {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: split plan produced no new files")
	}
	if len(newFileIDsByPath) > catalog.MaxRedirectTargets {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: got %d redirect targets, max %d", len(newFileIDsByPath), catalog.MaxRedirectTargets)
	}

	// Canonical ordering contract (see .cdr/memory/pending.md's "Canonical
	// newFileIDs ordering contract needed before 2b.3.6"): sort by NewPath,
	// matching ExecuteSplitBtreeInsert's own established convention, so
	// RedirectTargetIDs/stub content/entry order never silently depends on
	// Go's unspecified map iteration order.
	newPaths := make([]string, 0, len(newFileIDsByPath))
	for newPath := range newFileIDsByPath {
		newPaths = append(newPaths, newPath)
	}
	sort.Strings(newPaths)

	// newPathSizes gives each new path's content length (in bytes), needed
	// below both to populate wal.SplitCommitEntry.SizeBytes (so
	// RecoverSplitCommits can reconstruct a fresh catalog.CatalogRecord for
	// each new fileID without re-reading its content file) and to build that
	// same live-path CatalogRecord directly. Recomputed here via
	// extractSections rather than threading a return value out of
	// ExecuteSplitAllocateAndWrite (2b.3.1's already-verified signature),
	// which already computed the identical byte ranges once internally.
	newPathSizes := make(map[string]uint64, len(plan.Files))
	for _, proposal := range plan.Files {
		newPathSizes[proposal.NewPath] = uint64(len(extractSections(originalContent, proposal.SectionRanges)))
	}

	newFileIDs := make([]uint64, len(newPaths))
	entries := make([]wal.SplitCommitEntry, len(newPaths))
	for i, newPath := range newPaths {
		fileID := newFileIDsByPath[newPath]
		newFileIDs[i] = fileID
		entries[i] = wal.SplitCommitEntry{NewPath: newPath, FileID: fileID, SizeBytes: newPathSizes[newPath]}
	}

	// Write the redirect stub at the old path (2b.3.2's stub-content logic).
	// Still not visible: the catalog record's Status/RedirectTargetIDs have
	// not changed yet.
	//
	// Acquire the SAME per-fileID stripe lock ContentStore.Append's own
	// read-modify-write critical section takes (via the exported
	// LockFileContent method, since ContentStore.stripes itself stays
	// unexported outside engine/catalog), spanning this stub write through
	// the catalog Put and header-cache invalidation below. Fixes issue #13's
	// CHANGES_REQUESTED verification finding: prior to this lock, a
	// concurrent ReadPartial(originalFileID) could interleave between the
	// durable cat.Put and InvalidateHeaderCache and cache a soon-to-be-stale
	// header index. Deliberately released right after InvalidateHeaderCache
	// below (NOT held across the B+Tree inserts / graph edge appends that
	// follow in the same apply closure): those touch entirely different
	// locks (btree.Tree's own, graph.EdgeAppender's own) that ReadPartial
	// never takes, so widening this lock's scope to cover them would only
	// add unnecessary contention against unrelated ReadPartial(originalFileID)
	// callers with no correctness benefit. lockHeld tracks whether the
	// deferred release below still needs to run (it does on every early-return
	// error path; it does not once the closure's own release below has run).
	unlock := cs.LockFileContent(originalFileID)
	lockHeld := true
	defer func() {
		if lockHeld {
			unlock()
		}
	}()

	stubContent := buildRedirectStubContent(newFileIDs)
	if err := writeNewContentFile(cs.ContentPath(originalFileID), stubContent); err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: writing stub content for fileID %d: %w", originalFileID, err)
	}

	updated := rec
	updated.Status = catalog.StatusRedirect
	updated.RedirectTargetIDs = newFileIDs
	updated.SizeBytes = uint64(len(stubContent))

	encoded, err := updated.Encode()
	if err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: encoding fileID %d: %w", originalFileID, err)
	}

	if err := runAtomicCommitHook("before_commit_append"); err != nil {
		return catalog.CatalogRecord{}, err
	}

	commitRec := wal.NewSplitCommitRecord(wal.SplitCommitPayload{
		OriginalFileID:       originalFileID,
		OldPath:              oldPath,
		EncodedCatalogRecord: encoded,
		Entries:              entries,
	})

	// This wal.AppendAndApply call IS the transaction's single point of no
	// return: Writer.Append (durably fsyncing commitRec) happens first and
	// unconditionally; only once it has succeeded does the apply closure
	// below run cat.Put, the B+Tree inserts, and the graph edge appends, in
	// that order, with a hook checkpoint between each so
	// TestSplitAtomicCommit can deterministically simulate a crash at any of
	// those points and prove RecoverSplitCommits completes the transaction
	// regardless of exactly where it stopped.
	if _, err := wal.AppendAndApply(w, commitRec, func() error {
		if err := runAtomicCommitHook("after_commit_before_catalog_put"); err != nil {
			return err
		}
		if err := cat.Put(updated); err != nil {
			return fmt.Errorf("committing catalog record fileID %d: %w", originalFileID, err)
		}

		// BUGFIX (issue #14 / 2b.5's concurrent race-test implementation):
		// every new fileID produced by this split needs its OWN
		// catalog.CatalogRecord -- without one, cat.Get(newFileID) returns
		// ErrNotFound forever, and since catalog.ContentStore.Read/Append both
		// resolve fileID through the catalog first, every split-off file
		// becomes permanently unreadable/unappendable even though its content
		// file, B+Tree entry, and graph edges all exist. Status is
		// StatusActive: these are ordinary, immediately-usable files, not
		// stubs. This is part of the SAME WAL-covered apply closure as
		// originalFileID's own cat.Put above, so it shares its atomicity and
		// crash-durability; RecoverSplitCommits below mirrors this exact loop
		// for the replay path, using entry.SizeBytes (see
		// wal.SplitCommitEntry's doc comment).
		for _, entry := range entries {
			newRec := catalog.CatalogRecord{
				FileID:         entry.FileID,
				CurrentVersion: 0,
				SizeBytes:      entry.SizeBytes,
				Status:         catalog.StatusActive,
			}
			if err := cat.Put(newRec); err != nil {
				return fmt.Errorf("committing catalog record for new fileID %d (%q): %w", entry.FileID, entry.NewPath, err)
			}
		}

		// Test-only checkpoint (issue #13's 2b.4.1 fix cycle,
		// TestSectionIndexInvalidationConcurrent): fires in the exact window
		// between cat.Put committing the new Status=Redirect record and
		// InvalidateHeaderCache evicting the pre-split cache entry -- the
		// narrow gap where Bug 1 (missing cs.stripes lock across this
		// sequence) was originally observable. Still under the cs.stripes
		// lock acquired above, so with the fix in place any concurrent
		// ReadPartial(originalFileID) call is blocked out of this window
		// entirely; a test can use this hook to hold the window open and
		// confirm that.
		if err := runAtomicCommitHook("after_catalog_put_before_invalidate"); err != nil {
			return err
		}

		// Invalidate originalFileID's cached header-offset index (see issue #13's
		// subtask 2b.4.1) in this same apply step, immediately alongside the
		// catalog Status transition it pairs with: the redirect-stub content
		// (already written to disk above, before this WAL commit) is what any
		// subsequent ReadPartial(originalFileID) call must observe, never a
		// pre-split cache entry. Newly allocated fileIDs never had a cache entry,
		// so no invalidation is needed for them. Still under the cs.stripes lock
		// acquired above at this point, closing the fix cycle's Bug 1 race.
		cs.InvalidateHeaderCache(originalFileID)

		// Release the per-fileID content lock now: originalFileID's on-disk
		// content and catalog record are both consistent (redirect stub +
		// Status=Redirect) and the header cache has been evicted, so any
		// ReadPartial(originalFileID) call blocked on this stripe is now safe
		// to proceed and will observe post-split state. Everything from here
		// on (B+Tree inserts, graph edge appends) touches locks ReadPartial
		// never takes, so there is no correctness reason to keep holding this
		// one across them.
		unlock()
		lockHeld = false

		if err := runAtomicCommitHook("after_catalog_put_before_btree"); err != nil {
			return err
		}
		for _, newPath := range newPaths {
			normalizedNewPath := normalizeTopicPath(newPath)
			if err := tree.Insert(normalizedNewPath, newFileIDsByPath[newPath]); err != nil {
				return fmt.Errorf("repointing new path %q (normalized %q, fileID %d): %w", newPath, normalizedNewPath, newFileIDsByPath[newPath], err)
			}
		}
		if err := tree.Insert(normalizeTopicPath(oldPath), originalFileID); err != nil {
			return fmt.Errorf("repointing old path %q (normalized %q, fileID %d): %w", oldPath, normalizeTopicPath(oldPath), originalFileID, err)
		}

		if err := runAtomicCommitHook("after_btree_before_graph"); err != nil {
			return err
		}
		if err := appendSplitGraphEdges(appender, originalFileID, newFileIDs); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return catalog.CatalogRecord{}, fmt.Errorf("split: execute: atomic commit: %w", err)
	}

	// Every step above succeeded: the split is fully applied. Only now do we
	// release the guard, matching "release queued writers on commit" (see
	// this function's doc comment).
	guard.Release(originalFileID)

	return updated, nil
}

// appendSplitGraphEdges appends the full deterministic edge set a split
// between originalFileID and newFileIDs produces -- the same SPLIT_SIBLING
// all-pairs-complete-graph plus REDIRECT edge set ExecuteSplitGraphEdges
// (2b.3.5) appends -- using graph.EdgeAppender.AppendEdgeIfAbsent rather than
// AppendEdge, so that calling this helper more than once for the same
// (originalFileID, newFileIDs) pair (as RecoverSplitCommits may do, e.g. if
// invoked more than once, or after a crash left some but not all of these
// edges already durably appended) never produces duplicate edges.
func appendSplitGraphEdges(appender *graph.EdgeAppender, originalFileID uint64, newFileIDs []uint64) error {
	ids := make([]uint64, len(newFileIDs))
	copy(ids, newFileIDs)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for i := range ids {
		for j := range ids {
			if i == j {
				continue
			}
			edge := graph.Edge{Source: ids[i], Target: ids[j], Type: graph.EdgeSplitSibling}
			if err := appender.AppendEdgeIfAbsent(edge); err != nil {
				return fmt.Errorf("split: execute: graph edges: appending SPLIT_SIBLING edge %d->%d: %w", ids[i], ids[j], err)
			}
		}
	}

	for _, id := range ids {
		edge := graph.Edge{Source: originalFileID, Target: id, Type: graph.EdgeRedirect}
		if err := appender.AppendEdgeIfAbsent(edge); err != nil {
			return fmt.Errorf("split: execute: graph edges: appending REDIRECT edge %d->%d: %w", originalFileID, id, err)
		}
	}

	return nil
}

// RecoverSplitCommits is subtask 2b.3.6's crash-recovery replay pass: it
// scans the WAL rooted at walDir (the SAME WAL directory ExecuteSplitAtomic's
// w *wal.Writer is rooted at) for wal.RecordSplitCommit records via
// wal.Replay, and for each one found, re-applies its full effect --
// cat.Put(the final catalog record), every B+Tree insert (new paths, plus the
// old path's repoint), and every graph edge append -- via the exact same
// logic ExecuteSplitAtomic's own apply closure uses (catalog.Decode +
// cat.Put, tree.Insert, and the shared appendSplitGraphEdges helper).
//
// This directly closes the crash-recovery gap tracked in .cdr/memory/pending.md
// since 2b.3.4/2b.3.5 ("engine/graph edge-append records have no
// crash-recovery replay path"): graph edges produced by a split are now
// deterministically re-derivable from, and replayed as part of, this single
// WAL record type's recovery pass, going through engine/wal's Replay
// machinery exactly the way catalog/B+Tree WAL records already do -- not
// merely durable at the byte level as 2b.3.4/2b.3.5 left them.
//
// Every step RecoverSplitCommits performs is idempotent (cat.Put is a
// documented upsert; *btree.Tree.Insert is a documented upsert;
// appendSplitGraphEdges uses AppendEdgeIfAbsent), so calling
// RecoverSplitCommits more than once, or over a WAL directory where some
// commit records were already fully applied before a later crash, is always
// safe and converges on the same fully-applied state.
//
// wal.Replay itself dispatches on every record type it finds in walDir, not
// just wal.RecordSplitCommit; records of other types (e.g. RecordCatalogPut
// entries unrelated to a split) are simply skipped here, mirroring
// catalog.RecoverFromWAL's own documented "skip record types this function
// isn't responsible for" behavior. Consequently, full recovery of a WAL
// directory shared by engine/catalog and engine/split requires running BOTH
// catalog.RecoverFromWAL(cat, walDir) AND RecoverSplitCommits(walDir, cat,
// tree, appender) -- each owns its own record type(s) and neither asserts
// exclusive ownership of the directory.
//
// RecoverSplitCommits does not itself reconstruct *btree.Tree's root pointer
// or FileGuard's in-memory state; see ExecuteSplitAtomic's doc comment
// ("What is honestly NOT covered by this guarantee") for why those remain
// separate, pre-existing concerns.
func RecoverSplitCommits(walDir string, cat *catalog.Catalog, tree *btree.Tree, appender *graph.EdgeAppender) error {
	if cat == nil {
		return fmt.Errorf("split: recover: cat must not be nil")
	}
	if tree == nil {
		return fmt.Errorf("split: recover: tree must not be nil")
	}
	if appender == nil {
		return fmt.Errorf("split: recover: appender must not be nil")
	}

	err := wal.Replay(walDir, func(rec wal.TypedRecord) error {
		if rec.Type != wal.RecordSplitCommit {
			return nil
		}

		payload, err := rec.AsSplitCommit()
		if err != nil {
			return fmt.Errorf("decoding split commit payload: %w", err)
		}

		updated, err := catalog.Decode(payload.EncodedCatalogRecord)
		if err != nil {
			return fmt.Errorf("decoding catalog record for fileID %d: %w", payload.OriginalFileID, err)
		}
		if err := cat.Put(updated); err != nil {
			return fmt.Errorf("replaying catalog Put for fileID %d: %w", payload.OriginalFileID, err)
		}

		newFileIDs := make([]uint64, 0, len(payload.Entries))
		for _, entry := range payload.Entries {
			// Mirrors ExecuteSplitAtomic's own new-fileID catalog.CatalogRecord
			// creation (see its doc comment, "BUGFIX (issue #14 / 2b.5's
			// concurrent race-test implementation)"): replay must produce the
			// identical end state as the live path, so a crash-interrupted split
			// resumed via RecoverSplitCommits does not leave new fileIDs
			// catalog-orphaned either. cat.Put is a documented upsert, so this is
			// safe to re-run on every replay of an already-applied record.
			newRec := catalog.CatalogRecord{
				FileID:         entry.FileID,
				CurrentVersion: 0,
				SizeBytes:      entry.SizeBytes,
				Status:         catalog.StatusActive,
			}
			if err := cat.Put(newRec); err != nil {
				return fmt.Errorf("replaying catalog Put for new fileID %d (%q): %w", entry.FileID, entry.NewPath, err)
			}
			normalizedNewPath := normalizeTopicPath(entry.NewPath)
			if err := tree.Insert(normalizedNewPath, entry.FileID); err != nil {
				return fmt.Errorf("replaying B+Tree insert for %q (normalized %q, fileID %d): %w", entry.NewPath, normalizedNewPath, entry.FileID, err)
			}
			newFileIDs = append(newFileIDs, entry.FileID)
		}
		normalizedOldPath := normalizeTopicPath(payload.OldPath)
		if err := tree.Insert(normalizedOldPath, payload.OriginalFileID); err != nil {
			return fmt.Errorf("replaying B+Tree repoint of old path %q (normalized %q, fileID %d): %w", payload.OldPath, normalizedOldPath, payload.OriginalFileID, err)
		}

		if err := appendSplitGraphEdges(appender, payload.OriginalFileID, newFileIDs); err != nil {
			return fmt.Errorf("replaying graph edges for fileID %d: %w", payload.OriginalFileID, err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("split: recover: replaying split commits in %s: %w", walDir, err)
	}
	return nil
}
