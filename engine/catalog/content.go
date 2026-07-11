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

// SplitTriggerFunc is the shape of the size-threshold detection hook ContentStore.Append
// invokes on every append (subtask 4.5.3.1, issue #40), given fileID and the pre/post-append
// content sizes, returning whether this specific append crossed a split-eligibility size
// threshold.
//
// ContentStore deliberately does NOT import engine/split directly to obtain this hook's
// production implementation: engine/split already imports engine/catalog (in
// engine/split/execute.go and engine/split/orchestrate.go, for CatalogRecord/status types),
// so a reverse import from engine/catalog would be a circular import — verified empirically:
// an internal `package catalog` test file importing engine/split fails to build with "import
// cycle not allowed in test". Because Go function types are structurally, not nominally,
// typed, engine/split.Trigger.Detect's logic can be wired in from outside without either
// package importing the other's types here: a composition root that imports BOTH packages
// (see engine/cmd/smokeserver/main.go's wiring) constructs a real *split.Trigger and installs
// an adapter closure — e.g. `func(fileID, old, new uint64) bool { sig, ok :=
// trig.Detect(fileID, old, new); ...; return ok }` — via SetSplitTrigger. This is what makes
// a threshold crossing actually surface a split signal in production code paths (the
// compiled server binary), not just in engine/split/trigger_test.go, while keeping
// engine/catalog and engine/split's existing import direction (split -> catalog) unchanged.
//
// A nil SplitTriggerFunc (the default, e.g. for callers/tests that never call
// SetSplitTrigger) preserves Append's original behavior: an inline comparison against
// ContentStore's own splitThresholdBytes field. See Append's doc comment.
type SplitTriggerFunc func(fileID, oldSizeBytes, newSizeBytes uint64) bool

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

	// splitTrigger is the optional (subtask 4.5.3.1, issue #40) hook Append invokes,
	// when non-nil, in place of the local splitThresholdBytes comparison, to decide
	// whether a given append crossed a split-eligibility size threshold. Installed via
	// SetSplitTrigger. A nil splitTrigger (the default returned by OpenContentStore)
	// preserves the original inline-comparison behavior. See SplitTriggerFunc's doc
	// comment for why this is a plain callback rather than a direct engine/split import.
	splitTrigger SplitTriggerFunc

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

// SetSplitTrigger installs fn as the hook Append uses, on every subsequent call, to decide
// whether an append crossed a split-eligibility size threshold (subtask 4.5.3.1, issue #40),
// in place of ContentStore's own local splitThresholdBytes comparison. Passing nil reverts
// to that local-comparison default. See SplitTriggerFunc's doc comment for the intended
// production wiring (a *engine/split.Trigger-backed adapter installed by a composition root
// such as engine/cmd/smokeserver/main.go) and why this indirection exists (avoiding a
// circular import between engine/catalog and engine/split).
//
// SetSplitTrigger is not safe to call concurrently with Append against the same
// ContentStore; callers should install the hook once, before the ContentStore is shared
// across goroutines (matching how OpenContentStore's other fields are established before
// use).
func (cs *ContentStore) SetSplitTrigger(fn SplitTriggerFunc) {
	cs.splitTrigger = fn
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
//
// Duplicate-fileID semantics (subtask 4.5.5.4): calling Create a second time for a
// fileID that already has a catalog record and/or content file is LEGAL and
// intentionally performs a full, last-write-wins overwrite — it is not guarded by any
// already-exists check. This is safe rather than corrupting because both halves of the
// write are themselves safe overwrites: writeContentFile always writes to a fresh temp
// file and atomically os.Rename's it over ContentPath(rec.FileID), so a second Create
// cleanly replaces the previous content file with no leaked file/inode and no
// torn/partial state ever observable by a concurrent Read (see writeContentFile's and
// Read's doc comments); and cs.cat.Put(rec) is Catalog's own documented upsert
// (delete-old-slot-then-reinsert, no history kept, no leaked page slots — see
// catalog.go's Put doc comment), not an in-place mutation that could leave a
// half-updated record. Net effect of two Creates for the same fileID: the SECOND
// call's data and rec entirely supersede the first's, byte-for-byte and field-for-field;
// nothing from the first call survives or leaks. Callers that need "create only if
// fileID does not already exist" semantics must check cs.cat.Get(rec.FileID) themselves
// before calling Create — Create itself does not perform that check.
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
// resulting size pushes the file from at-or-under the configured split
// threshold to strictly over it. It is false both before that crossing
// append (size still at or under the threshold) and on every append after
// it (size already over the threshold from a prior call), so callers see
// the signal fire exactly once per crossing.
//
// Subtask 4.5.3.1 (issue #40): this threshold-crossing decision is now made
// by cs.splitTrigger, if one has been installed via SetSplitTrigger — see
// SplitTriggerFunc's doc comment for why that is a plain callback rather
// than a direct engine/split import, and for how a composition root wires it
// to a real engine/split.Trigger (whose Detect/CrossesThreshold logic this
// hook exists to reuse rather than duplicate). If no hook has been
// installed (cs.splitTrigger == nil), Append falls back to its original
// local comparison against ContentStore's own splitThresholdBytes field
// (defaulted to defaultSplitThresholdBytes by OpenContentStore), preserving
// prior behavior for callers that never call SetSplitTrigger. Either way,
// Append itself still performs no actual splitting (see docs/LLD/split.md's
// "Trigger" section) — it only surfaces the signal for a future Epic 2B
// caller to act on.
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

	var thresholdCrossed bool
	if cs.splitTrigger != nil {
		// Subtask 4.5.3.1 (issue #40): delegate to the installed hook (production
		// callers back this with a real engine/split.Trigger.Detect/CrossesThreshold
		// call — see SplitTriggerFunc's doc comment) on every append, invoked exactly
		// once per call, same as the fallback comparison below.
		thresholdCrossed = cs.splitTrigger(fileID, oldSize, newSize)
	} else {
		thresholdCrossed = oldSize <= cs.splitThresholdBytes && newSize > cs.splitThresholdBytes
	}
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

// LockFileContent acquires cs.stripes[stripeFor(fileID)] -- the SAME
// per-fileID striped mutex Append's own read-modify-write critical section
// takes (see Append's doc comment) and ReadPartial takes for its read
// (see ReadPartial's doc comment) -- and returns an unlock function the
// caller MUST call exactly once (typically via defer) to release it.
//
// This is the fix for issue #13's CHANGES_REQUESTED verification finding
// (subtask 2b.4.1's fix cycle): engine/split/execute.go's
// ExecuteSplitRedirectStub and ExecuteSplitAtomic durably rewrite
// originalFileID's content (the redirect-stub write) and then invalidate its
// header-offset cache, but — before this method existed — did so without
// ever taking cs.stripes at all, leaving a real window where a concurrent
// ReadPartial(originalFileID) could interleave between the stub's durable
// cat.Put and the InvalidateHeaderCache call and observe (and re-cache) a
// still-valid-looking-but-about-to-be-stale header index.
//
// Exposing cs.stripes itself (the raw [numStripes]sync.Mutex array) to
// engine/split would be a much bigger interface break than necessary and
// would let callers outside this package take these locks in arbitrary,
// unreviewed order; LockFileContent instead gives split's execute.go the
// same mutual-exclusion guarantee Append gets internally through a single
// narrow, purpose-built method, keeping cs.stripes itself unexported.
//
// Lock ordering: this must be acquired BEFORE any nested call this package
// makes that itself takes a different lock further down the stack (e.g.
// cs.cat.Put, which takes Catalog's own, independent stripes array) —
// exactly the order Append already establishes and documents (cs.stripes,
// then, nested inside, cs.cat's own stripe for the same fileID). Callers
// holding this lock must not call back into anything that could try to
// re-acquire cs.stripes[stripeFor(fileID)] itself (this repo's
// sync.Mutex is non-reentrant); InvalidateHeaderCache is always safe to call
// while holding this lock because it only ever takes the separate,
// independent cs.headerCacheMu (see InvalidateHeaderCache's doc comment).
// wal.Writer's own internal locking is likewise always safe to enter while
// holding this lock, because Append already does exactly that (cs.stripes
// held across its own wal.AppendAndApply call), establishing cs.stripes ->
// wal.Writer-internal as the sole existing nesting order anywhere this lock
// is taken; this method does not change that order, only lets a second
// package (engine/split) participate in it correctly.
func (cs *ContentStore) LockFileContent(fileID uint64) (unlock func()) {
	stripe := stripeFor(fileID)
	cs.stripes[stripe].Lock()
	return cs.stripes[stripe].Unlock
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
// Note on split (updated by issue #13's 2b.4.1 fix cycle): engine/split/execute.go's
// ExecuteSplitRedirectStub and ExecuteSplitAtomic now ALSO take
// cs.stripes[stripeFor(originalFileID)] — via the exported LockFileContent
// method below, since cs.stripes itself stays unexported — across their own
// stub-content-write + cat.Put + InvalidateHeaderCache sequence, mirroring
// Append's own critical section exactly. This closes the real (narrow) race
// window the original 2b.4.1 implementation left open, where a ReadPartial
// call could interleave between a split's durable cat.Put and its
// InvalidateHeaderCache call and cache a soon-to-be-stale header index. See
// LockFileContent's doc comment for the full design rationale and lock-order
// reasoning.
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
