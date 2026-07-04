package mvcc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
)

func TestVersionWriter(t *testing.T) {
	t.Run("sequential", func(t *testing.T) {
		dir := t.TempDir()
		vw, err := NewVersionWriter(dir)
		if err != nil {
			t.Fatalf("NewVersionWriter: %v", err)
		}

		const fileID = uint64(7)
		const numWrites = 5

		var priorPaths []string
		var priorContents [][]byte
		var priorModTimes []time.Time

		for i := 1; i <= numWrites; i++ {
			data := []byte(fmt.Sprintf("content-%d", i))
			version, err := vw.WriteVersion(fileID, data)
			if err != nil {
				t.Fatalf("WriteVersion #%d: %v", i, err)
			}
			if version != uint64(i) {
				t.Fatalf("WriteVersion #%d: got version %d, want %d", i, version, i)
			}

			path := vw.VersionPath(fileID, version)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			if string(got) != string(data) {
				t.Fatalf("version %d content = %q, want %q", version, got, data)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s: %v", path, err)
			}

			// Assert every prior version file is untouched: same content, same
			// mtime, still present.
			for j, priorPath := range priorPaths {
				gotPrior, err := os.ReadFile(priorPath)
				if err != nil {
					t.Fatalf("re-reading prior version file %s after write #%d: %v", priorPath, i, err)
				}
				if string(gotPrior) != string(priorContents[j]) {
					t.Fatalf("prior version file %s content changed after write #%d: got %q, want %q",
						priorPath, i, gotPrior, priorContents[j])
				}
				priorInfo, err := os.Stat(priorPath)
				if err != nil {
					t.Fatalf("re-stat prior version file %s after write #%d: %v", priorPath, i, err)
				}
				if !priorInfo.ModTime().Equal(priorModTimes[j]) {
					t.Fatalf("prior version file %s mtime changed after write #%d: got %v, want %v",
						priorPath, i, priorInfo.ModTime(), priorModTimes[j])
				}
			}

			priorPaths = append(priorPaths, path)
			priorContents = append(priorContents, data)
			priorModTimes = append(priorModTimes, info.ModTime())
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		dir := t.TempDir()
		vw, err := NewVersionWriter(dir)
		if err != nil {
			t.Fatalf("NewVersionWriter: %v", err)
		}

		const fileID = uint64(99)
		const numGoroutines = 50

		var wg sync.WaitGroup
		versions := make([]uint64, numGoroutines)
		errs := make([]error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				data := []byte(fmt.Sprintf("concurrent-%d", idx))
				v, err := vw.WriteVersion(fileID, data)
				versions[idx] = v
				errs[idx] = err
			}(i)
		}
		wg.Wait()

		seen := make(map[uint64]bool, numGoroutines)
		for i, err := range errs {
			if err != nil {
				t.Fatalf("goroutine %d: WriteVersion error: %v", i, err)
			}
			v := versions[i]
			if v == 0 {
				t.Fatalf("goroutine %d: got version 0, want >= 1", i)
			}
			if seen[v] {
				t.Fatalf("version %d handed out more than once (collision)", v)
			}
			seen[v] = true
		}

		if len(seen) != numGoroutines {
			t.Fatalf("got %d distinct versions, want %d", len(seen), numGoroutines)
		}
		// Every version in {1..numGoroutines} must be present: strictly
		// increasing per fileID with no gaps and no collisions.
		for v := uint64(1); v <= uint64(numGoroutines); v++ {
			if !seen[v] {
				t.Fatalf("version %d missing from concurrent writes; got set %v", v, seen)
			}
		}

		// All version files must exist on disk with their own distinct content.
		for v := uint64(1); v <= uint64(numGoroutines); v++ {
			path := vw.VersionPath(fileID, v)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("version file for version %d missing: %v", v, err)
			}
		}
	})

	t.Run("cold_start_reopen", func(t *testing.T) {
		dir := t.TempDir()
		const fileID = uint64(5)

		vw1, err := NewVersionWriter(dir)
		if err != nil {
			t.Fatalf("NewVersionWriter (first open): %v", err)
		}
		for i := 1; i <= 3; i++ {
			if _, err := vw1.WriteVersion(fileID, []byte(fmt.Sprintf("v%d", i))); err != nil {
				t.Fatalf("WriteVersion (first open) #%d: %v", i, err)
			}
		}

		// Simulate a process restart: construct a brand-new VersionWriter (fresh
		// in-memory state) over the same on-disk content directory.
		vw2, err := NewVersionWriter(dir)
		if err != nil {
			t.Fatalf("NewVersionWriter (second open): %v", err)
		}
		version, err := vw2.WriteVersion(fileID, []byte("v4"))
		if err != nil {
			t.Fatalf("WriteVersion (second open): %v", err)
		}
		if version != 4 {
			t.Fatalf("after cold restart, WriteVersion returned %d, want 4 (must resume from existing on-disk versions)", version)
		}

		// Prior versions (from before the simulated restart) must still be present
		// and untouched.
		for i := uint64(1); i <= 3; i++ {
			path := vw1.VersionPath(fileID, i)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading pre-restart version %d: %v", i, err)
			}
			want := fmt.Sprintf("v%d", i)
			if string(got) != want {
				t.Fatalf("pre-restart version %d content = %q, want %q", i, got, want)
			}
		}
	})

	t.Run("distinct fileIDs do not interfere", func(t *testing.T) {
		dir := t.TempDir()
		vw, err := NewVersionWriter(dir)
		if err != nil {
			t.Fatalf("NewVersionWriter: %v", err)
		}

		v1, err := vw.WriteVersion(4, []byte("a"))
		if err != nil {
			t.Fatalf("WriteVersion(4): %v", err)
		}
		v2, err := vw.WriteVersion(42, []byte("b"))
		if err != nil {
			t.Fatalf("WriteVersion(42): %v", err)
		}
		if v1 != 1 {
			t.Fatalf("WriteVersion(4) = %d, want 1", v1)
		}
		if v2 != 1 {
			t.Fatalf("WriteVersion(42) = %d, want 1 (must not be confused with fileID 4's versions)", v2)
		}
	})
}

// newTestCatalog opens a fresh catalog.Catalog backed by an isolated t.TempDir() path,
// mirroring engine/catalog/catalog_test.go's helper of the same shape.
func newTestCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.dat")
	fm, err := catalog.Open(path)
	if err != nil {
		t.Fatalf("catalog.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := fm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return catalog.NewCatalog(fm)
}

// countVersionFiles returns how many "<fileID>.vN.md" files currently exist in vw's
// content directory for fileID, along with the highest N among them (0 if none). This
// counts EVERY version file ever durably written for fileID, including ones a losing
// CAS attempt orphaned (never referenced by CurrentVersion) — see CommitVersion's doc
// comment on why the final CurrentVersion is expected to equal this count/max exactly.
func countVersionFiles(t *testing.T, vw *VersionWriter, fileID uint64) (count int, maxVersion uint64) {
	t.Helper()
	entries, err := os.ReadDir(vw.dir)
	if err != nil {
		t.Fatalf("reading content dir: %v", err)
	}
	prefix := fmt.Sprintf("%d.v", fileID)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, versionFileSuffix) {
			continue
		}
		middle := strings.TrimSuffix(strings.TrimPrefix(name, prefix), versionFileSuffix)
		n, err := strconv.ParseUint(middle, 10, 64)
		if err != nil {
			continue
		}
		count++
		if n > maxVersion {
			maxVersion = n
		}
	}
	return count, maxVersion
}

// TestCurrentVersionCAS exercises subtask 2a.1.2's acceptance criteria: N concurrent
// writers race to CommitVersion the SAME fileID; the catalog's CurrentVersion is only
// ever updated via CAS after each writer's version file is durably written, a losing
// CAS retries (rather than corrupting state or silently dropping data), and the final
// CurrentVersion matches exactly one consistent, well-defined outcome (see
// CommitVersion's doc comment in write.go for the precise "no lost updates" contract
// this asserts: with fresh-version-file-per-retry semantics, the final CurrentVersion
// equals the highest version file number that exists on disk once every writer has
// completed, not merely the count of goroutines, since a losing attempt's version file
// is retained on disk but orphaned rather than reused).
func TestCurrentVersionCAS(t *testing.T) {
	dir := t.TempDir()
	vw, err := NewVersionWriter(dir)
	if err != nil {
		t.Fatalf("NewVersionWriter: %v", err)
	}
	cat := newTestCatalog(t)

	const fileID = uint64(123)
	if err := cat.Put(catalog.CatalogRecord{
		FileID:         fileID,
		CurrentVersion: 0,
		Status:         catalog.StatusActive,
	}); err != nil {
		t.Fatalf("seeding initial catalog record: %v", err)
	}

	const numGoroutines = 30

	var wg sync.WaitGroup
	versions := make([]uint64, numGoroutines)
	errs := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("writer-%d", idx))
			v, err := vw.CommitVersion(cat, fileID, data)
			versions[idx] = v
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// No writer's CommitVersion call may fail or silently drop its write: every one
	// of the N concurrent calls must eventually succeed with its own version number.
	seen := make(map[uint64]bool, numGoroutines)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: CommitVersion error: %v", i, err)
		}
		v := versions[i]
		if v == 0 {
			t.Fatalf("goroutine %d: got version 0, want >= 1", i)
		}
		if seen[v] {
			t.Fatalf("version %d returned by more than one goroutine (collision/lost update)", v)
		}
		seen[v] = true
	}
	if len(seen) != numGoroutines {
		t.Fatalf("got %d distinct successfully-committed versions, want %d (no lost updates)", len(seen), numGoroutines)
	}

	// The final catalog CurrentVersion must equal the highest version file number
	// present on disk for fileID once every writer has completed (see CommitVersion's
	// doc comment for why: the temporally-last successful CAS always corresponds to
	// the temporally-last WriteVersion call, which is always the highest number).
	fileCount, maxOnDisk := countVersionFiles(t, vw, fileID)
	if fileCount < numGoroutines {
		t.Fatalf("expected at least %d version files on disk (one per goroutine, plus any retries), got %d", numGoroutines, fileCount)
	}

	rec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("Get final record: %v", err)
	}
	if rec.CurrentVersion != maxOnDisk {
		t.Fatalf("final CurrentVersion = %d, want %d (highest version file on disk)", rec.CurrentVersion, maxOnDisk)
	}
	if !seen[rec.CurrentVersion] {
		t.Fatalf("final CurrentVersion %d was not among the versions successfully returned by any goroutine", rec.CurrentVersion)
	}

	// The version file CurrentVersion now points at must contain exactly the data
	// written by whichever goroutine's CommitVersion call returned that version
	// number — no torn/corrupted content.
	var winnerIdx = -1
	for i, v := range versions {
		if v == rec.CurrentVersion {
			winnerIdx = i
			break
		}
	}
	if winnerIdx == -1 {
		t.Fatalf("no goroutine index found for final CurrentVersion %d", rec.CurrentVersion)
	}
	path := vw.VersionPath(fileID, rec.CurrentVersion)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading final current version file %s: %v", path, err)
	}
	want := fmt.Sprintf("writer-%d", winnerIdx)
	if string(got) != want {
		t.Fatalf("final current version file content = %q, want %q", got, want)
	}
}
