package mvcc

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
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
