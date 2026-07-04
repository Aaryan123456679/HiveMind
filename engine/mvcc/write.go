package mvcc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
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
func (vw *VersionWriter) CommitVersion(cat *catalog.Catalog, fileID uint64, data []byte) (uint64, error) {
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

		ok, _, err := cat.CompareAndSwapCurrentVersion(fileID, expected, version)
		if err != nil {
			return 0, fmt.Errorf("mvcc: commit version: CAS for fileID %d: %w", fileID, err)
		}
		if ok {
			return version, nil
		}
		// Lost the race: some other CommitVersion call's CAS already advanced
		// CurrentVersion past `expected`. Loop and retry against the winner's
		// current state with a fresh version file, rather than corrupting state
		// or silently dropping this call's write.
	}
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
