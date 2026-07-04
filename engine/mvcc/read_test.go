package mvcc

import (
	"testing"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
)

// TestSnapshotRead exercises subtask 2a.1.3's acceptance criteria: a reader that
// starts before a concurrent write commits must continue to see its
// originally-snapshotted version for the entire read, even after the pointer
// advances (docs/LLD/mvcc.md's "Read path" contract).
func TestSnapshotRead(t *testing.T) {
	dir := t.TempDir()
	vw, err := NewVersionWriter(dir)
	if err != nil {
		t.Fatalf("NewVersionWriter: %v", err)
	}
	cat := newTestCatalog(t)

	const fileID = uint64(55)
	if err := cat.Put(catalog.CatalogRecord{
		FileID:         fileID,
		CurrentVersion: 0,
		Status:         catalog.StatusActive,
	}); err != nil {
		t.Fatalf("seeding initial catalog record: %v", err)
	}

	v1Content := []byte("version-one-content")
	v1, err := vw.CommitVersion(cat, fileID, v1Content)
	if err != nil {
		t.Fatalf("CommitVersion (v1): %v", err)
	}
	if v1 != 1 {
		t.Fatalf("CommitVersion (v1) = %d, want 1", v1)
	}

	snap, err := NewSnapshot(cat, vw, fileID)
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	if snap.Version() != 1 {
		t.Fatalf("Snapshot.Version() = %d, want 1 (pinned before any v2 commit)", snap.Version())
	}

	// Channels orchestrating the interleaving, mirroring
	// engine/wal/record_test.go's TestFsyncBeforeApply / engine/catalog/content_test.go's
	// TestContentCreate before/after channel-handoff technique: pause the read after
	// the version is already pinned (snapshot taken above) but before the version
	// file's bytes are actually read from disk, let a concurrent CommitVersion race
	// to completion in that window, then resume the read.
	pausedAtRead := make(chan struct{})
	writerDone := make(chan struct{})

	readResult := make(chan []byte, 1)
	readErr := make(chan error, 1)

	go func() {
		data, err := snap.readWithHook(func() {
			close(pausedAtRead)
			<-writerDone
		})
		readResult <- data
		readErr <- err
	}()

	// Wait until the read has captured its version and is paused right before
	// reading the version file, then commit a brand-new version concurrently,
	// advancing CurrentVersion out from under the already-started read.
	<-pausedAtRead

	v2Content := []byte("version-two-content-committed-mid-read")
	v2, err := vw.CommitVersion(cat, fileID, v2Content)
	if err != nil {
		t.Fatalf("CommitVersion (v2, concurrent with paused read): %v", err)
	}
	if v2 != 2 {
		t.Fatalf("CommitVersion (v2) = %d, want 2", v2)
	}

	rec, err := cat.Get(fileID)
	if err != nil {
		t.Fatalf("Get after v2 commit: %v", err)
	}
	if rec.CurrentVersion != 2 {
		t.Fatalf("CurrentVersion after v2 commit = %d, want 2", rec.CurrentVersion)
	}

	// Let the paused read resume now that the pointer has advanced.
	close(writerDone)

	got := <-readResult
	if err := <-readErr; err != nil {
		t.Fatalf("Snapshot.Read (resumed after concurrent v2 commit): %v", err)
	}
	if string(got) != string(v1Content) {
		t.Fatalf("Read() after concurrent mid-read commit = %q, want pre-write content %q", got, v1Content)
	}

	// The already-taken snapshot must still report version 1, even though
	// CurrentVersion is now 2.
	if snap.Version() != 1 {
		t.Fatalf("Snapshot.Version() after concurrent v2 commit = %d, want 1 (unchanged)", snap.Version())
	}

	// Sanity check: snapshotting is not a permanent global freeze — a FRESH snapshot
	// taken now must observe the new version.
	gotFresh, err := SnapshotRead(cat, vw, fileID)
	if err != nil {
		t.Fatalf("SnapshotRead (fresh, after v2 commit): %v", err)
	}
	if string(gotFresh) != string(v2Content) {
		t.Fatalf("fresh SnapshotRead after v2 commit = %q, want %q", gotFresh, v2Content)
	}
}

// TestSnapshotReadNoVersionCommitted asserts Read fails cleanly (rather than panicking
// or returning zero-value success) when a catalog record exists but no version has
// ever been committed via CommitVersion (CurrentVersion == 0), since
// VersionPath(fileID, 0) was never written by WriteVersion (numbering always starts
// at 1).
func TestSnapshotReadNoVersionCommitted(t *testing.T) {
	dir := t.TempDir()
	vw, err := NewVersionWriter(dir)
	if err != nil {
		t.Fatalf("NewVersionWriter: %v", err)
	}
	cat := newTestCatalog(t)

	const fileID = uint64(56)
	if err := cat.Put(catalog.CatalogRecord{
		FileID:         fileID,
		CurrentVersion: 0,
		Status:         catalog.StatusActive,
	}); err != nil {
		t.Fatalf("seeding initial catalog record: %v", err)
	}

	if _, err := SnapshotRead(cat, vw, fileID); err == nil {
		t.Fatalf("SnapshotRead with no committed version: got nil error, want an error")
	}
}
