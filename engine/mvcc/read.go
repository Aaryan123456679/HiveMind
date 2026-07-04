package mvcc

import (
	"fmt"
	"os"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
)

// Snapshot pins a fileID to a specific version number, captured from the catalog's
// CurrentVersion pointer at one instant in time (see NewSnapshot). This is the
// read-side building block described in docs/LLD/mvcc.md's "Read path" section:
// "Readers snapshot the current version pointer at the start of the request and read
// that specific version to completion, regardless of concurrent writers advancing the
// pointer afterward."
//
// Correctness reasoning (see architecture-discovery.md for the full writeup): once a
// Snapshot has captured version N, reading N's content file requires NO additional
// locking, because:
//
//  1. Version files are immutable once written — VersionWriter.WriteVersion (2a.1.1)
//     always assigns a brand-new, never-before-used version number; no code path ever
//     reopens an existing version's file to rewrite it.
//  2. Nothing in this codebase deletes old version files yet — reclaiming versions no
//     longer referenced by any in-flight snapshot is reference-counted epoch-based
//     garbage collection, an explicitly separate, not-yet-implemented later subtask
//     (docs/LLD/mvcc.md's "Garbage collection" section; see epic 2A's epoch-GC
//     subtask).
//
// Therefore a snapshotted version N's file is guaranteed to exist, byte-for-byte
// unchanged, for as long as this Snapshot lives, even while a concurrent CommitVersion
// call races ahead writing version N+1 and CASing CurrentVersion forward: that
// concurrent writer only ever touches its own new file and the catalog's
// CurrentVersion field, never the already-snapshotted N's file.
type Snapshot struct {
	vw      *VersionWriter
	fileID  uint64
	version uint64
}

// NewSnapshot captures fileID's CurrentVersion from cat at this exact instant — the
// "snapshot" moment the read-path contract requires — and returns a Snapshot pinned to
// that version number. It does not touch the content directory or read any bytes yet;
// call Read (or SnapshotRead for the one-shot combined form) to do that.
//
// cat must already hold a CatalogRecord for fileID (e.g. from Catalog.Put), matching
// VersionWriter.CommitVersion's own precondition on the write side.
func NewSnapshot(cat *catalog.Catalog, vw *VersionWriter, fileID uint64) (*Snapshot, error) {
	rec, err := cat.Get(fileID)
	if err != nil {
		return nil, fmt.Errorf("mvcc: new snapshot: reading catalog record for fileID %d: %w", fileID, err)
	}
	return &Snapshot{vw: vw, fileID: fileID, version: rec.CurrentVersion}, nil
}

// Version returns the version number this Snapshot is pinned to. It never changes
// after NewSnapshot returns, regardless of how far CurrentVersion advances afterward.
func (s *Snapshot) Version() uint64 {
	return s.version
}

// Read reads this Snapshot's pinned version's content file to completion. Because the
// version number was already fixed at NewSnapshot time, Read always returns that exact
// version's content — even if some concurrent writer commits a newer version between
// NewSnapshot and Read returning, or while Read is itself in progress.
//
// If this Snapshot's pinned version is 0 (the catalog record exists but no version was
// ever committed via VersionWriter.CommitVersion), Read fails: VersionPath(fileID, 0)
// was never written by WriteVersion, which always starts numbering at 1.
func (s *Snapshot) Read() ([]byte, error) {
	return s.readWithHook(nil)
}

// readWithHook is Read's real implementation, with an internal test-only seam:
// afterSnapshotBeforeRead, if non-nil, is invoked after the version number is already
// pinned (captured earlier, in NewSnapshot) but before the version file's bytes are
// actually read from disk. This lets read_test.go pause a read at exactly that point,
// race a concurrent CommitVersion to completion, then resume the read and assert it
// still returns the pre-commit content — the same before/after channel-handoff
// technique engine/wal/record_test.go's TestFsyncBeforeApply and
// engine/catalog/content_test.go's TestContentCreate (via content.go's createWithHook)
// already use to prove ordering guarantees from within the same package.
func (s *Snapshot) readWithHook(afterSnapshotBeforeRead func()) ([]byte, error) {
	if afterSnapshotBeforeRead != nil {
		afterSnapshotBeforeRead()
	}

	path := s.vw.VersionPath(s.fileID, s.version)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mvcc: snapshot read: reading version %d for fileID %d: %w", s.version, s.fileID, err)
	}
	return data, nil
}

// SnapshotRead is a one-shot convenience combining NewSnapshot and Read: it captures
// fileID's current version from cat and immediately reads that version's content to
// completion. Equivalent to calling NewSnapshot followed by Read, for callers that
// don't need to hold onto the Snapshot (e.g. to inspect Version()) separately.
func SnapshotRead(cat *catalog.Catalog, vw *VersionWriter, fileID uint64) ([]byte, error) {
	snap, err := NewSnapshot(cat, vw, fileID)
	if err != nil {
		return nil, err
	}
	return snap.Read()
}
