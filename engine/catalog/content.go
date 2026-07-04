package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
// Phase 1 (pre-Epic 2A) assumption: no additional locking is introduced here beyond what
// Catalog and wal.Writer already provide internally. A single logical "create a file"
// operation (content write + WAL record + catalog Put) is not itself made atomic against
// a concurrent second operation on the SAME fileID racing it; that concurrency hardening
// is explicitly deferred, matching the precedent set by catalog.go's own documented
// known-gap comments for this phase.
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

	return &ContentStore{dir: dir, cat: cat, w: w, splitThresholdBytes: defaultSplitThresholdBytes}, nil
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
func (cs *ContentStore) Append(fileID uint64, data []byte) (bool, error) {
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

		return nil
	}); err != nil {
		return false, fmt.Errorf("catalog: content append: %w", err)
	}

	thresholdCrossed := oldSize <= cs.splitThresholdBytes && newSize > cs.splitThresholdBytes
	return thresholdCrossed, nil
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
