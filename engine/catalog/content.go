package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// contentDirName is the fixed subdirectory (relative to a ContentStore's root) that holds
// every topic file's content, matching this subtask's acceptance criterion's literal path
// shape: "content/<fileID>.v1.md".
const contentDirName = "content"

// contentVersionSuffix is the fixed version segment used by every content file name this
// subtask writes. Task 1.4.1 is deliberately pre-MVCC and single-version only (see the
// issue title: "Single-version .md content read/write"); it always writes/overwrites the
// "v1" file regardless of CatalogRecord.CurrentVersion. Multi-version content file naming
// (content/<fileID>.v<N>.md, keyed off CurrentVersion) is out of scope here and left to
// whichever later subtask under this epic introduces MVCC-aware content versioning.
const contentVersionSuffix = ".v1.md"

// defaultSplitThresholdBytes is the default split-trigger threshold used by
// Append's threshold-crossing signal (subtask 1.4.3), matching the ~8KB /
// ~2000 tokens default documented in docs/LLD/split.md's "Trigger" section.
// This is deliberately just a size threshold, not the real auto-split logic
// (engine/split/ is scaffold-only as of this subtask); actual split execution
// is out of scope until Epic 2B. Documented override point: callers needing a
// different threshold (e.g. tests exercising crossing behavior cheaply) may
// set ContentStore.splitThresholdBytes directly after OpenContentStore.
const defaultSplitThresholdBytes = 8 * 1024

// ContentStore is the on-disk content (topic file body) I/O layer that sits alongside
// Catalog: Catalog owns a fileID's metadata record, ContentStore owns the actual .md
// bytes for that fileID. See docs/LLD/catalog.md's "wal/" cross-reference: every catalog
// mutation must be logged in the WAL before it is applied, a guarantee ContentStore.Create
// provides by building on engine/wal's AppendAndApply idiom (the same fsync-before-apply
// primitive engine/wal/record_test.go's TestFsyncBeforeApply demonstrates).
//
// Concurrency: Append performs a read-modify-write of fileID's content file (read
// existing bytes, append, write the combined result), which is unsafe to run
// concurrently against itself for the SAME fileID without serialization — two
// concurrent Appends could both read the same "existing" bytes and each write back
// a result that only reflects their own appended data, silently losing the other's
// update (and there is no error to surface this: cat.Put would happily accept
// whichever write landed last). ContentStore therefore reuses this repo's
// documented striped-mutex convention (docs/LLD/catalog.md's "Striped mutexes (~256
// stripes, hashed by fileID) instead of one global lock", the same pattern
// Catalog.stripes implements at catalog.go's Catalog doc comment) via its own
// independent stripes array (see below) — independent from, not shared with,
// Catalog's own stripes, because Append's critical section calls cs.cat.Put
// internally, and cs.cat.Put takes Catalog's OWN stripe lock for rec.FileID; reusing
// the exact same lock instance would deadlock a non-reentrant sync.Mutex on that
// call. Create does not need this same protection: it is only ever called once per
// fileID, with a freshly-allocated fileID that by construction (engine/idalloc's
// monotonic Next()) cannot yet have a concurrent second Create call racing it for
// the same fileID; there is no existing content to race a read-modify-write against.
// Read does not need it either: it never performs a read-modify-write, and
// writeContentFile's write-to-temp-then-rename technique makes a single Read always
// observe either the fully-old or fully-new content, never a torn/partial one.
type ContentStore struct {
	dir string // absolute/relative path to the "content" directory itself
	cat *Catalog
	w   *wal.Writer

	// splitThresholdBytes is the size (in bytes) Append compares the
	// post-append content length against to decide whether to report a
	// threshold-crossing signal. Defaulted to defaultSplitThresholdBytes by
	// OpenContentStore; overridable directly by callers (e.g. tests) that
	// need a different threshold. See Append's doc comment.
	splitThresholdBytes uint64

	// stripes serializes Append's read-modify-write critical section per fileID,
	// keyed by stripeFor(fileID) (the same hashing scheme Catalog.stripes uses,
	// reused here as its own independent array — see the ContentStore doc comment
	// above for why it must be independent rather than shared with cs.cat's
	// stripes). Concurrent Appends to DIFFERENT fileIDs still proceed without
	// contending on each other's stripe, preserving this repo's "unrelated files
	// never contend on the same lock" design goal.
	stripes [numStripes]sync.Mutex

	// headerCacheMu guards headerCache. It is a SINGLE dedicated mutex, deliberately
	// NOT keyed/striped by fileID like cs.stripes: headerCache is one plain Go map
	// shared across every fileID, and Go maps are never safe for concurrent access
	// regardless of whether two callers happen to touch different keys, so a single
	// lock protecting the whole map is required (striping by fileID would let two
	// different fileIDs landing in different stripes race on the same map object).
	// Kept as its own lock instance, independent from cs.stripes, specifically so it
	// can be taken from WITHIN an already-held cs.stripes[stripe] critical section
	// (as Append does) without risking a non-reentrant sync.Mutex deadlock — see the
	// ContentStore doc comment's existing discussion of the analogous cs.stripes vs.
	// cs.cat.stripes separation for the same underlying reason.
	headerCacheMu sync.Mutex

	// headerCache is an in-memory, per-fileID cache of ReadPartial's computed
	// markdown header-offset index (see HeaderOffset), populated lazily on first
	// ReadPartial call and evicted by InvalidateHeaderCache whenever a transaction
	// changes that fileID's content boundaries (Append, or a split's redirect-stub
	// rewrite). A missing entry simply means "not cached yet" — ReadPartial always
	// recomputes from current on-disk content on a miss, so this cache can never
	// itself be a source of a wrong answer, only of an avoidable recompute. See
	// docs/LLD/catalog.md's and docs/LLD/split.md's "Section-index staleness" known
	// risk, which this field and its invalidation call sites (in Append here, and in
	// engine/split/execute.go's ExecuteSplitRedirectStub/ExecuteSplitAtomic) resolve.
	headerCache map[uint64][]HeaderOffset
}

// HeaderOffset represents one markdown ATX header line (one to six leading '#'
// characters followed by whitespace or end-of-line, per the CommonMark ATX heading
// rule this repo's minimal parser implements) found in a topic file's content,
// paired with its byte offset within that content. ReadPartial returns a fileID's
// full set of HeaderOffsets, in the order they appear in the content, so a caller
// can resolve a "read starting at this header" request to a byte offset without
// re-scanning the whole file.
type HeaderOffset struct {
	// Header is the header line's text, including its leading '#' markers, with
	// trailing whitespace/newline trimmed (e.g. "## Some Section").
	Header string
	// Offset is the byte offset, within the content ReadPartial computed this
	// HeaderOffset from, of the header line's very first character.
	Offset int
}

// computeHeaderOffsets scans content line-by-line and returns one HeaderOffset per
// ATX markdown header line found (a line whose first non-header characters are one
// to six '#' runes immediately followed by whitespace or end-of-line), in the order
// the lines appear in content. It performs no caching itself; ReadPartial is the
// only caller and owns the cache.
func computeHeaderOffsets(content []byte) []HeaderOffset {
	var headers []HeaderOffset

	lineStart := 0
	for lineStart <= len(content) {
		lineEnd := lineStart
		for lineEnd < len(content) && content[lineEnd] != '\n' {
			lineEnd++
		}
		line := content[lineStart:lineEnd]

		if isATXHeaderLine(line) {
			headers = append(headers, HeaderOffset{
				Header: strings.TrimRight(string(line), " \t\r"),
				Offset: lineStart,
			})
		}

		if lineEnd >= len(content) {
			break
		}
		lineStart = lineEnd + 1
	}

	return headers
}

// isATXHeaderLine reports whether line (with no trailing newline) is an ATX
// markdown header line: one to six '#' characters, then either end-of-line or a
// whitespace character. Leading whitespace before the '#' run is NOT stripped
// (matching the simplest possible reading of "header line" for this cache; a more
// permissive CommonMark-compliant parser, e.g. tolerating up to three leading
// spaces, is left to whichever future subtask needs it — see the doc comment on
// HeaderOffset).
func isATXHeaderLine(line []byte) bool {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i > 6 {
		return false
	}
	if i == len(line) {
		return true
	}
	return line[i] == ' ' || line[i] == '\t'
}

// OpenContentStore creates (if necessary) a "content" directory under root and returns a
// ContentStore backed by cat (for catalog visibility) and w (for WAL-before-apply
// durability). cat and w must already be open; ContentStore does not own their lifecycle
// (it never closes them).
func OpenContentStore(root string, cat *Catalog, w *wal.Writer) (*ContentStore, error) {
	if cat == nil {
		return nil, fmt.Errorf("catalog: OpenContentStore: cat must not be nil")
	}
	if w == nil {
		return nil, fmt.Errorf("catalog: OpenContentStore: w must not be nil")
	}

	dir := filepath.Join(root, contentDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("catalog: OpenContentStore: creating content dir %s: %w", dir, err)
	}

	return &ContentStore{
		dir:                 dir,
		cat:                 cat,
		w:                   w,
		splitThresholdBytes: defaultSplitThresholdBytes,
		headerCache:         make(map[uint64][]HeaderOffset),
	}, nil
}

// ContentPath returns the on-disk path of fileID's (single, pre-MVCC) content file:
// <root>/content/<fileID>.v1.md.
func (cs *ContentStore) ContentPath(fileID uint64) string {
	return filepath.Join(cs.dir, fmt.Sprintf("%d%s", fileID, contentVersionSuffix))
}

// Create is the content store's create/write path: it durably logs rec as a catalog Put
// mutation to the WAL, and ONLY THEN writes data to disk at ContentPath(rec.FileID) and
// makes rec visible via cat.Put — in that order, enforced structurally by
// wal.AppendAndApply (not just by convention), matching the WAL-before-apply invariant in
// docs/LLD/wal.md and docs/LLD/catalog.md.
//
// It returns the WAL offset the catalog-Put record was appended at, alongside any error.
// If the WAL append itself fails, neither the content file nor the catalog record is
// touched. If the WAL append succeeds but writing the content file or the catalog Put
// fails, the WAL record is already durable (matching wal.AppendAndApply's documented
// error-handling contract) — recovery/replay of that record is a later subtask's concern,
// not this one's.
func (cs *ContentStore) Create(rec CatalogRecord, data []byte) (int64, error) {
	return cs.createWithHook(rec, data, nil)
}

// createWithHook is Create's real implementation, with an internal test-only seam:
// afterWALBeforeApply, when non-nil, runs after the WAL record has been durably appended
// but strictly before the content file is written or rec becomes visible via cat.Put. This
// lets content_test.go observe (from within the same package, without duplicating this
// wiring) that the WAL record precedes catalog visibility, the same before/after
// observation technique engine/wal/record_test.go's TestFsyncBeforeApply uses to prove
// wal.AppendAndApply's own ordering guarantee.
func (cs *ContentStore) createWithHook(rec CatalogRecord, data []byte, afterWALBeforeApply func()) (int64, error) {
	if rec.FileID == InvalidFileID {
		return 0, fmt.Errorf("catalog: content create: invalid fileID %d", rec.FileID)
	}

	encoded, err := rec.Encode()
	if err != nil {
		return 0, fmt.Errorf("catalog: content create: encoding fileID %d: %w", rec.FileID, err)
	}

	walRec := wal.NewCatalogPutRecord(rec.FileID, encoded)

	offset, err := wal.AppendAndApply(cs.w, walRec, func() error {
		if afterWALBeforeApply != nil {
			afterWALBeforeApply()
		}

		if err := cs.writeContentFile(rec.FileID, data); err != nil {
			return fmt.Errorf("writing content file for fileID %d: %w", rec.FileID, err)
		}

		if err := cs.cat.Put(rec); err != nil {
			return fmt.Errorf("committing catalog record for fileID %d: %w", rec.FileID, err)
		}

		return nil
	})
	if err != nil {
		return offset, fmt.Errorf("catalog: content create: %w", err)
	}
	return offset, nil
}

// Read returns the current full markdown content for fileID exactly as last
// written by Create (byte-for-byte).
//
// Read resolves fileID through the catalog first (cs.cat.Get), mirroring the
// catalog-is-source-of-truth convention Create already relies on for visibility:
// a fileID with no catalog record is reported as ErrNotFound (wrapped, matching
// catalog.go's Get/Delete convention), the same sentinel content_test.go already
// asserts against via cat.Get. Only once the catalog confirms the fileID exists
// does Read touch disk at ContentPath(fileID).
//
// If the catalog record exists but the content file itself is missing or
// unreadable, that is reported as a distinct (non-ErrNotFound) error: it
// indicates an internal inconsistency (e.g. a crash between catalog Put and
// content file write in some future non-atomic path, or WAL replay not yet
// implemented) rather than "this fileID was never created", so callers must be
// able to tell the two apart.
//
// Task 1.4.2 is pre-MVCC, single-version only (see content.go's package-level
// doc comment and contentVersionSuffix): Read always serves the single "v1"
// file regardless of rec.CurrentVersion; version-aware path resolution is
// deferred to the later MVCC subtask.
func (cs *ContentStore) Read(fileID uint64) ([]byte, error) {
	if _, err := cs.cat.Get(fileID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("catalog: content read: %w: fileID %d", ErrNotFound, fileID)
		}
		return nil, fmt.Errorf("catalog: content read: looking up fileID %d: %w", fileID, err)
	}

	data, err := os.ReadFile(cs.ContentPath(fileID))
	if err != nil {
		return nil, fmt.Errorf("catalog: content read: reading content file for fileID %d: %w", fileID, err)
	}
	return data, nil
}

// Append is the content store's append/mutate path (subtask 1.4.3): it reads
// fileID's current content, appends data to it, durably logs the resulting
// catalog record (with an updated SizeBytes) as a catalog Put mutation to the
// WAL, and ONLY THEN writes the combined content to disk and makes the
// updated record visible via cat.Put — the same WAL-before-apply discipline
// Create already provides, built on the same wal.AppendAndApply primitive.
//
// Like Read, Append resolves fileID through the catalog first; a fileID with
// no catalog record is reported as a wrapped ErrNotFound.
//
// Append returns thresholdCrossed=true exactly on the one call whose
// resulting size pushes the file from at-or-under ContentStore's configured
// split threshold (splitThresholdBytes, defaulted to
// defaultSplitThresholdBytes) to strictly over it. It is false both before
// that crossing append (size still at or under the threshold) and on every
// append after it (size already over the threshold from a prior call), so
// callers see the signal fire exactly once per crossing. This is
// deliberately just a signal/stub for a future Epic 2B caller to act on
// (see docs/LLD/split.md's "Trigger" section); Append itself never invokes
// engine/split or performs any actual splitting.
//
// Task 1.4.3 is pre-MVCC, single-version only, matching Create/Read: Append
// always mutates the single "v1" content file regardless of
// rec.CurrentVersion.
//
// Concurrency: Append's read-existing/append/write-back critical section is
// serialized per fileID via cs.stripes (keyed by stripeFor(fileID)), so
// concurrent Append calls against the SAME fileID cannot lose an update; see
// the ContentStore doc comment for why this is an independent stripes array
// rather than reusing Catalog's own. Concurrent Appends to DIFFERENT fileIDs
// still proceed in parallel (different stripes, in the common case).
func (cs *ContentStore) Append(fileID uint64, data []byte) (bool, error) {
	stripe := stripeFor(fileID)
	cs.stripes[stripe].Lock()
	defer cs.stripes[stripe].Unlock()

	rec, err := cs.cat.Get(fileID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, fmt.Errorf("catalog: content append: %w: fileID %d", ErrNotFound, fileID)
		}
		return false, fmt.Errorf("catalog: content append: looking up fileID %d: %w", fileID, err)
	}

	existing, err := os.ReadFile(cs.ContentPath(fileID))
	if err != nil {
		return false, fmt.Errorf("catalog: content append: reading content file for fileID %d: %w", fileID, err)
	}

	oldSize := uint64(len(existing))
	newContent := append(append([]byte(nil), existing...), data...)
	newSize := uint64(len(newContent))

	updatedRec := rec
	updatedRec.SizeBytes = newSize

	encoded, err := updatedRec.Encode()
	if err != nil {
		return false, fmt.Errorf("catalog: content append: encoding fileID %d: %w", fileID, err)
	}

	walRec := wal.NewCatalogPutRecord(fileID, encoded)

	if _, err := wal.AppendAndApply(cs.w, walRec, func() error {
		if err := cs.writeContentFile(fileID, newContent); err != nil {
			return fmt.Errorf("writing content file for fileID %d: %w", fileID, err)
		}

		if err := cs.cat.Put(updatedRec); err != nil {
			return fmt.Errorf("committing catalog record for fileID %d: %w", fileID, err)
		}

		// Invalidate fileID's cached header-offset index (if any) as part of this
		// same apply step, so no subsequent ReadPartial call can observe a cache
		// entry computed against the content this Append just replaced. Safe to
		// call while still holding cs.stripes[stripe] above: InvalidateHeaderCache
		// only ever takes cs.headerCacheMu, a separate lock instance — see the
		// ContentStore doc comment on headerCacheMu for why that can never deadlock
		// here. See docs/LLD/catalog.md's "Section-index staleness" known risk.
		cs.InvalidateHeaderCache(fileID)

		return nil
	}); err != nil {
		return false, fmt.Errorf("catalog: content append: %w", err)
	}

	thresholdCrossed := oldSize <= cs.splitThresholdBytes && newSize > cs.splitThresholdBytes
	return thresholdCrossed, nil
}

// InvalidateHeaderCache evicts fileID's cached markdown header-offset index (see
// HeaderOffset and ReadPartial), if one is currently cached. It is a no-op if
// fileID has no cached entry. Safe to call from any lock context, including from
// another package (e.g. engine/split/execute.go's split-commit apply closures) or
// from within an already-held cs.stripes[stripe] critical section (e.g. Append,
// above): InvalidateHeaderCache only ever takes cs.headerCacheMu, never
// cs.stripes, so it cannot deadlock against either caller.
//
// This is the mechanism by which "any transaction that changes file boundaries
// (split or append) invalidates the affected file's header-offset cache in the
// same atomic transaction" (issue #13, subtask 2b.4.1) is satisfied: every
// call site that durably changes a fileID's content (Append here; and
// ExecuteSplitRedirectStub/ExecuteSplitAtomic in engine/split/execute.go) calls
// this from within its own WAL-covered apply closure, so the eviction only takes
// effect if and when that transaction actually commits.
func (cs *ContentStore) InvalidateHeaderCache(fileID uint64) {
	cs.headerCacheMu.Lock()
	delete(cs.headerCache, fileID)
	cs.headerCacheMu.Unlock()
}

// ReadPartial returns fileID's markdown header-offset index: one HeaderOffset per
// ATX header line found in fileID's CURRENT content, in the order the headers
// appear. It resolves fileID through the catalog first, exactly like Read (a
// fileID with no catalog record is reported as a wrapped ErrNotFound), then
// serves from cs.headerCache if a valid (i.e. not since invalidated) entry
// exists, computing and caching it on a miss.
//
// Concurrency: ReadPartial takes cs.stripes[stripeFor(fileID)] for its own
// critical section — the SAME per-fileID lock Append's read-modify-write section
// takes — so a ReadPartial call can never interleave with a concurrent Append (or
// a split's redirect-stub rewrite, which also takes this stripe implicitly via
// its own InvalidateHeaderCache call happening inside that transaction's own,
// separately-serialized apply closure... see the note below) for the SAME
// fileID. In practice this means: any ReadPartial call that starts after a
// content-changing transaction for fileID has returned is guaranteed to observe
// that transaction's invalidation and recompute from the new content, never a
// stale cache entry — the exact guarantee issue #13's acceptance criteria
// requires ("ReadPartial never serves offsets against stale content").
//
// Note on split: engine/split/execute.go's ExecuteSplitRedirectStub and
// ExecuteSplitAtomic do NOT take cs.stripes for originalFileID at all (they call
// the self-locking, cs.stripes-independent InvalidateHeaderCache instead, and
// otherwise serialize their own critical section via FileGuard/Orchestrator, a
// different mechanism entirely from cs.stripes). A ReadPartial call that races a
// split's redirect-stub content write itself (i.e. calls os.ReadFile on
// cs.ContentPath(originalFileID) at some arbitrary point during the split's
// non-atomic content-write phase) could in principle observe partially-written
// content if it landed mid-write — but writeNewContentFile's temp-file+rename
// technique (the same one cs.writeContentFile uses) makes any single os.ReadFile
// always observe either the fully-old or fully-new content, never a torn file,
// so this is not a correctness gap for ReadPartial's cache: whichever
// consistent snapshot it reads and caches is a valid answer for SOME instant in
// time, and the next InvalidateHeaderCache call (from the split's own apply
// closure, once its WAL commit has durably applied) still correctly evicts it.
func (cs *ContentStore) ReadPartial(fileID uint64) ([]HeaderOffset, error) {
	stripe := stripeFor(fileID)
	cs.stripes[stripe].Lock()
	defer cs.stripes[stripe].Unlock()

	if _, err := cs.cat.Get(fileID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("catalog: content read partial: %w: fileID %d", ErrNotFound, fileID)
		}
		return nil, fmt.Errorf("catalog: content read partial: looking up fileID %d: %w", fileID, err)
	}

	cs.headerCacheMu.Lock()
	if cached, ok := cs.headerCache[fileID]; ok {
		cs.headerCacheMu.Unlock()
		result := make([]HeaderOffset, len(cached))
		copy(result, cached)
		return result, nil
	}
	cs.headerCacheMu.Unlock()

	content, err := os.ReadFile(cs.ContentPath(fileID))
	if err != nil {
		return nil, fmt.Errorf("catalog: content read partial: reading content file for fileID %d: %w", fileID, err)
	}

	computed := computeHeaderOffsets(content)

	cs.headerCacheMu.Lock()
	cs.headerCache[fileID] = computed
	cs.headerCacheMu.Unlock()

	result := make([]HeaderOffset, len(computed))
	copy(result, computed)
	return result, nil
}

// writeContentFile durably writes data to fileID's content path. It writes to a temporary
// sibling file first and renames it into place, so a crash mid-write can never leave a
// torn/partial content file visible at the final path (rename is atomic on the same
// filesystem, matching this repo's general durability posture elsewhere, e.g.
// engine/catalog/file.go's WriteAt+Sync convention for the catalog's own pages).
func (cs *ContentStore) writeContentFile(fileID uint64, data []byte) error {
	finalPath := cs.ContentPath(fileID)

	tmp, err := os.CreateTemp(cs.dir, fmt.Sprintf("%d.v1.*.md.tmp", fileID))
	if err != nil {
		return fmt.Errorf("creating temp content file: %w", err)
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
