package mvcc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// contentDirName is the fixed subdirectory (relative to VersionWriter's root) that
// holds every fileID's versioned content, matching the on-disk shape documented in
// docs/LLD/mvcc.md's "Write path" section: "content/<fileID>.vN.md".
const contentDirName = "content"

// versionFileSuffix separates a fileID from its version number in a content file's
// name, e.g. "42.v3.md".
const versionFileSuffix = ".md"

// fileState tracks the last version number written for a single fileID, plus the
// mutex that serializes "determine next N -> write file" into one atomic critical
// section for that fileID. Zero value has next == 0, meaning "not yet determined";
// WriteVersion resolves it lazily from disk the first time a given fileID is touched
// by this VersionWriter, so numbering stays correct even across process restarts
// (see architecture-discovery.md's "Design decision: monotonic numbering source").
type fileState struct {
	mu   sync.Mutex
	next uint64 // 0 means "unknown, must scan disk"; otherwise the last version written
}

// VersionWriter creates new immutable content versions for a fileID under
// "<root>/content/<fileID>.vN.md", with N strictly increasing per fileID and never
// reused. It is the write-side building block described in docs/LLD/mvcc.md's "Write
// path"; it deliberately does NOT touch the catalog's CurrentVersion pointer or the
// WAL (that CAS/durability wiring is a later subtask - see architecture-discovery.md's
// "out_of_scope" notes). VersionWriter is safe for concurrent use by multiple
// goroutines, including concurrent writes to the SAME fileID.
type VersionWriter struct {
	dir string // absolute/relative path to the "content" directory itself

	// states holds one *fileState per fileID that has been written through this
	// VersionWriter, so repeated writes to the same fileID don't need to rescan the
	// directory every time. Keyed by fileID (uint64), values are *fileState.
	// A sync.Map (rather than a single map + mutex) lets unrelated fileIDs make
	// progress without contending on a shared lock, consistent with this repo's
	// "unrelated files never contend on the same lock" convention (see
	// engine/catalog/content.go's ContentStore doc comment on independent stripes).
	states sync.Map

	// commitLocks serializes CommitVersion's WAL-log-then-CAS critical section
	// (see walCAS below) per fileID, keyed by fileID (uint64), values are
	// *sync.Mutex, lazily created via LoadOrStore on first use — same shape as
	// states above, but a distinct sync.Map: states only guards WriteVersion's
	// "determine next N" numbering step, while commitLocks guards the separate
	// "verify CompareAndSwapCurrentVersion's expected still holds -> WAL-log ->
	// apply the CAS" step that follows it. Holding commitLocks across that
	// entire step is what lets CommitVersion safely reuse the plain
	// RecordCatalogPut WAL record type (via wal.NewCatalogPutRecord) without
	// ever logging a record for a CAS attempt that will not actually be
	// applied: because Catalog.CompareAndSwapCurrentVersion has no other
	// caller in this codebase, serializing every attempt for a given fileID
	// through this lock guarantees that once a goroutine has re-verified
	// `expected` under it, no concurrent goroutine can have raced it before
	// the CAS call inside apply runs. See CommitVersion's and walCAS's doc
	// comments below, and docs/LLD/wal.md's WAL-before-apply invariant /
	// docs/LLD/mvcc.md's "every version-pointer CAS ... goes through the WAL
	// first".
	commitLocks sync.Map
}

// NewVersionWriter creates (if necessary) the "content" directory under root and
// returns a VersionWriter backed by it.
func NewVersionWriter(root string) (*VersionWriter, error) {
	dir := filepath.Join(root, contentDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mvcc: NewVersionWriter: creating content dir %s: %w", dir, err)
	}
	return &VersionWriter{dir: dir}, nil
}

// VersionPath returns the on-disk path for fileID's version N: <root>/content/<fileID>.vN.md.
func (vw *VersionWriter) VersionPath(fileID, version uint64) string {
	return filepath.Join(vw.dir, fmt.Sprintf("%d.v%d%s", fileID, version, versionFileSuffix))
}

// WriteVersion durably writes data as a brand-new, immutable version file for fileID
// and returns the version number (N) it was written under. N is strictly increasing
// per fileID: the very first call for a given fileID (in this process, on a fresh or
// pre-existing content directory) returns 1 plus whatever the highest version already
// on disk for that fileID is (0 if none), and every subsequent call for the same
// fileID returns one more than the last version handed out - never colliding, even
// under concurrent calls from multiple goroutines against the same fileID.
//
// Because each call writes to a path that embeds its own, never-before-used version
// number, prior version files are never opened for writing again and so are left
// byte-for-byte untouched by later writes.
func (vw *VersionWriter) WriteVersion(fileID uint64, data []byte) (uint64, error) {
	stateAny, _ := vw.states.LoadOrStore(fileID, &fileState{})
	state := stateAny.(*fileState)

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.next == 0 {
		latest, err := vw.scanLatestVersion(fileID)
		if err != nil {
			return 0, fmt.Errorf("mvcc: write version: scanning existing versions for fileID %d: %w", fileID, err)
		}
		state.next = latest
	}

	version := state.next + 1

	if err := vw.writeVersionFile(fileID, version, data); err != nil {
		return 0, fmt.Errorf("mvcc: write version: fileID %d version %d: %w", fileID, version, err)
	}

	state.next = version
	return version, nil
}

// CommitVersion durably writes data as a brand-new version for fileID (via
// WriteVersion) and then atomically publishes it as fileID's current version in cat,
// via Catalog.CompareAndSwapCurrentVersion. This is the CAS wiring
// docs/LLD/mvcc.md's "Write path" describes: the version file is durably written
// FIRST; only once that succeeds is the catalog's CurrentVersion pointer swapped, and
// the swap itself is a CAS keyed on the CurrentVersion this call observed when it
// started, never a blind overwrite. cat must already hold a CatalogRecord for fileID
// (e.g. from Catalog.Put) before CommitVersion is called.
//
// WAL-before-apply (subtask 2a.1.4): the CAS itself is now a catalog mutation logged
// to w, the shared WAL, BEFORE it becomes visible in cat, matching docs/LLD/wal.md's
// invariant ("every mutation to the catalog ... must be logged to the WAL before it
// is applied in memory or on disk") and docs/LLD/mvcc.md's "every version-pointer CAS
// is a catalog mutation and therefore goes through the WAL first". This reuses the
// same wal.AppendAndApply + wal.NewCatalogPutRecord primitives engine/catalog/content.go's
// Create/Append already use, structurally guaranteeing the WAL record is durable
// (fsynced) before the swap is applied — see walCAS below for how a losing CAS
// attempt is prevented from ever reaching the WAL in the first place, so that a crash
// between the WAL append and the swap's visibility can always be recovered cleanly by
// catalog.RecoverFromWAL, which replays RecordCatalogPut records unconditionally in
// on-disk order (no new WAL record type is needed: once durable, a version-pointer CAS
// is just "this fileID's CatalogRecord is now X", indistinguishable from any other
// catalog Put).
//
// Concurrency / "no lost updates" contract for N concurrent CommitVersion calls on
// the SAME fileID:
//
//   - Every call's data is durably written to its own, never-reused version file
//     (WriteVersion's per-fileID monotonic numbering guarantees this — see its doc
//     comment), regardless of whether that call's CAS attempt subsequently wins or
//     loses.
//   - Every call EVENTUALLY returns successfully: if this call's CAS is refused
//     because a concurrent CommitVersion's CAS already advanced CurrentVersion out
//     from under it (the CurrentVersion this call observed via cat.Get no longer
//     matches), CommitVersion does NOT give up, retry the same stale CAS, or silently
//     drop the write. It loops: re-reads the catalog record to observe the winner's
//     new CurrentVersion, writes a FRESH version file (via another WriteVersion call,
//     since VersionWriter never reuses or rewrites a version number once assigned),
//     and re-attempts the CAS against the winner's new state — repeating until it
//     wins or a non-CAS error occurs (e.g. fileID not found).
//   - A consequence of always writing a brand-new version file per retry (rather than
//     reusing the prior attempt's number) is that a "losing" attempt's version file is
//     left orphaned on disk: never referenced by CurrentVersion, but never deleted or
//     corrupted either. It is exactly the kind of unreachable-but-still-present old
//     version docs/LLD/mvcc.md's "Garbage collection" section describes as eligible
//     for later reclamation by a background compactor; CommitVersion itself never
//     deletes version files.
//   - Therefore, after N concurrent CommitVersion calls on one fileID all complete
//     successfully, the fileID's final CurrentVersion equals the version number of
//     whichever call's CAS completed last in real time. Because WriteVersion assigns
//     version numbers in the exact order calls acquire its internal per-fileID lock,
//     and a goroutine only stops retrying once it succeeds, the very last WriteVersion
//     call issued for this fileID (globally, across every attempt AND every retry) is
//     guaranteed to belong to the goroutine whose CAS succeeds last (if it had lost,
//     it would have looped and written yet another, even-higher-numbered version
//     instead of returning) — so the final CurrentVersion always equals the highest
//     version number that exists on disk for this fileID once all N calls have
//     returned. No caller's write is ever lost: each of the N calls' data is captured
//     durably as some retained version file (whether or not that particular file ends
//     up referenced by CurrentVersion), and CurrentVersion always reflects the
//     temporally last one to actually complete its full write+CAS sequence.
func (vw *VersionWriter) CommitVersion(cat *catalog.Catalog, w *wal.Writer, fileID uint64, data []byte) (uint64, error) {
	return vw.commitVersionWithHook(cat, w, fileID, data, nil)
}

// commitVersionWithHook is CommitVersion's real implementation, with an internal
// test-only seam: afterWALBeforeApply, when non-nil, runs after the version-pointer
// CAS's WAL record has been durably appended but strictly before
// Catalog.CompareAndSwapCurrentVersion makes the swap visible. This lets
// write_test.go observe (without duplicating this wiring) that the WAL record
// precedes catalog visibility, the same before/after observation technique
// engine/catalog/content_test.go's TestContentCreate and engine/mvcc/read_test.go's
// TestSnapshotRead already use.
func (vw *VersionWriter) commitVersionWithHook(cat *catalog.Catalog, w *wal.Writer, fileID uint64, data []byte, afterWALBeforeApply func()) (uint64, error) {
	for {
		rec, err := cat.Get(fileID)
		if err != nil {
			return 0, fmt.Errorf("mvcc: commit version: reading catalog record for fileID %d: %w", fileID, err)
		}
		expected := rec.CurrentVersion

		version, err := vw.WriteVersion(fileID, data)
		if err != nil {
			return 0, fmt.Errorf("mvcc: commit version: writing version file for fileID %d: %w", fileID, err)
		}

		committed, err := vw.walCAS(cat, w, fileID, expected, version, afterWALBeforeApply)
		if err != nil {
			return 0, fmt.Errorf("mvcc: commit version: WAL-logged CAS for fileID %d: %w", fileID, err)
		}
		if committed {
			return version, nil
		}
		// Lost the race: some other CommitVersion call's CAS already advanced
		// CurrentVersion past `expected`, detected by walCAS before anything was
		// logged to the WAL. Loop and retry against the winner's current state
		// with a fresh version file, rather than corrupting state or silently
		// dropping this call's write.
	}
}

// walCAS performs the WAL-before-apply step of a single CommitVersion attempt for
// fileID: swapping CurrentVersion from expected to newVersion, but only after
// durably logging that swap to w. It returns (true, nil) if the swap was logged and
// applied, or (false, nil) if the attempt lost the race before ever touching the WAL
// (the caller should retry with a fresh version file), or a non-nil error for any
// other failure.
//
// This is the piece that makes reusing the plain RecordCatalogPut WAL record type
// (rather than inventing a version-CAS-specific one) correct: unlike
// engine/catalog/content.go's Create/Append, whose "apply" (cat.Put) always succeeds
// once the WAL record is durable, Catalog.CompareAndSwapCurrentVersion can lose a
// race. Logging first and discovering the loss only afterward (inside apply) would
// leave a durable WAL record describing a mutation that was never actually applied
// live — and because catalog.RecoverFromWAL replays CatalogPut records
// unconditionally (last write wins, no CAS re-validation on replay), a crash landing
// between that "doomed" record and the real winning attempt's later record could
// cause recovery to reconstruct the losing/stale value instead of the intended final
// one.
//
// walCAS closes that gap by acquiring fileID's commitLocks entry and holding it across
// the ENTIRE remaining sequence: re-reading cat.Get to re-verify expected still holds,
// WAL-logging the resulting record via wal.AppendAndApply, and only then calling
// Catalog.CompareAndSwapCurrentVersion inside apply. Because
// Catalog.CompareAndSwapCurrentVersion has no other caller in this codebase, this lock
// serializes every attempt for fileID: by the time a goroutine has re-verified
// expected and decided to log, no concurrent goroutine can advance CurrentVersion out
// from under it before the CAS call inside apply runs, so that CAS is guaranteed to
// succeed. A stale `expected` is therefore always caught by the re-check BEFORE any
// WAL write happens, not after.
func (vw *VersionWriter) walCAS(cat *catalog.Catalog, w *wal.Writer, fileID, expected, newVersion uint64, afterWALBeforeApply func()) (bool, error) {
	lockAny, _ := vw.commitLocks.LoadOrStore(fileID, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	rec, err := cat.Get(fileID)
	if err != nil {
		return false, fmt.Errorf("re-reading catalog record for fileID %d: %w", fileID, err)
	}
	if rec.CurrentVersion != expected {
		// Lost the race before ever touching the WAL: some other CommitVersion
		// call already advanced CurrentVersion past `expected` while this call
		// was writing its version file. Nothing has been logged, so there is
		// nothing to undo; the caller retries with a fresh version file.
		return false, nil
	}

	rec.CurrentVersion = newVersion
	encoded, err := rec.Encode()
	if err != nil {
		return false, fmt.Errorf("encoding updated catalog record for fileID %d: %w", fileID, err)
	}

	walRec := wal.NewCatalogPutRecord(fileID, encoded)

	_, err = wal.AppendAndApply(w, walRec, func() error {
		if afterWALBeforeApply != nil {
			afterWALBeforeApply()
		}

		ok, _, casErr := cat.CompareAndSwapCurrentVersion(fileID, expected, newVersion)
		if casErr != nil {
			return fmt.Errorf("applying CAS for fileID %d: %w", fileID, casErr)
		}
		if !ok {
			// Should be unreachable: this goroutine is the only one that can be
			// inside this fileID's commitLocks critical section right now, and
			// it just re-verified expected==rec.CurrentVersion above under the
			// same lock, so no other CAS can have intervened. Surfaced as a
			// hard error (not silently retried here) since it would indicate a
			// real synchronization bug — e.g. something calling
			// Catalog.CompareAndSwapCurrentVersion directly, outside
			// CommitVersion/walCAS.
			return fmt.Errorf("internal inconsistency: CAS refused for fileID %d despite matching expected %d under commit lock", fileID, expected)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// writeVersionFile durably writes data to fileID's version N path. It writes a
// temporary sibling file first and renames it into place, so a crash mid-write can
// never leave a torn/partial version file visible at the final path (rename is
// atomic on the same filesystem), matching this repo's general durability posture
// elsewhere (e.g. engine/catalog/content.go's writeContentFile).
func (vw *VersionWriter) writeVersionFile(fileID, version uint64, data []byte) error {
	finalPath := vw.VersionPath(fileID, version)

	tmp, err := os.CreateTemp(vw.dir, fmt.Sprintf("%d.v%d.*.md.tmp", fileID, version))
	if err != nil {
		return fmt.Errorf("creating temp version file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing temp version file %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("syncing temp version file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp version file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, finalPath, err)
	}
	return nil
}

// scanLatestVersion scans vw.dir for existing "<fileID>.v<N>.md" files and returns
// the highest N found, or 0 if fileID has no version files on disk yet. This is what
// makes numbering correct across process restarts: a freshly-constructed
// VersionWriter over a pre-populated content directory resumes numbering from
// whatever is already there instead of colliding with it.
func (vw *VersionWriter) scanLatestVersion(fileID uint64) (uint64, error) {
	prefix := fmt.Sprintf("%d.v", fileID)

	entries, err := os.ReadDir(vw.dir)
	if err != nil {
		return 0, fmt.Errorf("reading content dir %s: %w", vw.dir, err)
	}

	var latest uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, versionFileSuffix) {
			continue
		}

		// prefix already includes the ".v" separator, so it cannot ambiguously
		// match a different fileID's file (e.g. prefix "4.v" never matches
		// "42.v3.md", since the character after "4" there is "2", not ".").
		middle := strings.TrimSuffix(strings.TrimPrefix(name, prefix), versionFileSuffix)
		// middle must be a bare integer (e.g. "3"); anything else (including the
		// ".<rand>.md.tmp" shape of an in-flight temp file, whose suffix wouldn't
		// even match versionFileSuffix) is skipped.
		n, err := strconv.ParseUint(middle, 10, 64)
		if err != nil {
			continue
		}
		if n > latest {
			latest = n
		}
	}
	return latest, nil
}
