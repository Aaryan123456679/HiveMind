# Plan — 2a.1.2

1. Add `Catalog.CompareAndSwapCurrentVersion(fileID, expected, newVersion uint64) (bool,
   CatalogRecord, error)` to `engine/catalog/catalog.go`. Implementation mirrors `Put`'s
   existing pattern (stripe lock -> index lookup -> readSlot/tombstone/insert -> index
   update) but adds the compare step: after decoding the current record, only proceed
   with tombstone+insert+index-update if `rec.CurrentVersion == expected`; otherwise
   return `(false, rec, nil)` unchanged so the caller can see the winner's state.

2. Add `VersionWriter.CommitVersion(cat *catalog.Catalog, fileID uint64, data []byte)
   (uint64, error)` to `engine/mvcc/write.go`. Loop: `cat.Get` to observe expected
   CurrentVersion -> `vw.WriteVersion` to durably write a new version file -> `cat.
   CompareAndSwapCurrentVersion(fileID, expected, version)`. If CAS succeeds, return the
   version. If it fails (lost race, no error), loop and retry from scratch (fresh Get,
   fresh WriteVersion, fresh CAS attempt) rather than reusing the stale expected value
   or the already-written version file.

3. Clarify "no lost updates" contract (this directly affects what the test asserts):
   Because every retry writes a BRAND NEW version file (per the task brief's explicit
   direction), the final CurrentVersion after N concurrent CommitVersion calls is NOT
   simply N — it equals the highest version-file number on disk once all N calls have
   returned. Reasoning: WriteVersion assigns version numbers in strict temporal order of
   when each call acquires its internal per-fileID mutex; a goroutine only stops calling
   WriteVersion once its CAS succeeds, so in the terminal state (all N goroutines
   finished) the globally-last WriteVersion call must belong to whichever CAS completed
   last, and that CAS's version becomes the final CurrentVersion. This is a fully
   deterministic invariant to test: after all writers finish, compare
   `cat.Get(fileID).CurrentVersion` against the max version-file number found on disk
   for fileID.
   "No lost updates" is asserted as: every one of the N goroutines' CommitVersion calls
   returns success with a DISTINCT version number (no writer's data is silently dropped
   or double-counted), and the version file CurrentVersion ultimately points at contains
   exactly the payload of whichever goroutine returned that version number (no
   torn/corrupted content).

4. Test `TestCurrentVersionCAS` in `engine/mvcc/write_test.go`:
   - Seed a CatalogRecord (CurrentVersion=0) for one fileID.
   - Launch 30 goroutines, each calling `CommitVersion(cat, fileID, uniqueData)`
     concurrently.
   - Assert: no errors; all 30 returned version numbers are distinct (no collisions/
     lost updates); final `CurrentVersion` equals the max version-file number found on
     disk (helper `countVersionFiles`); that final version's on-disk content matches the
     one goroutine whose returned version equals it.
   - Run under `-race`, plus a `-count=5` repeat for extra confidence given timing-
     dependent retry behavior.

5. Self-consistency: gofmt, go vet, go build ./..., go test ./mvcc/... -race -v
   -count=1, go test ./mvcc/... -race -run TestCurrentVersionCAS -count=5, go test
   ./catalog/... -race -count=1 (regression check on Catalog), go test ./... (full repo
   regression sweep).

6. One local commit, no push. No self-verification — hand off to /cdr:verify.
