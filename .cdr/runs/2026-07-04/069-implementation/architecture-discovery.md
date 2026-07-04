# Architecture discovery — 2a.1.2

## Index-first
- `.cdr/index/task.jsonl`: task-2a.1.1 verified (PASS_WITH_COMMENTS), commit 34bc95d,
  handoff notes CAS/WAL wiring deferred to this subtask.
- No existing `.cdr/index` entry for a catalog CAS method — confirmed via reading
  `engine/catalog/catalog.go` directly (small, already-read file, no LLD index entry
  covers method-level API).

## engine/mvcc/write.go (2a.1.1, pre-existing)
- `VersionWriter` writes `<root>/content/<fileID>.vN.md`, N strictly increasing per
  fileID via `WriteVersion`, serialized per-fileID by `fileState.mu` (a `sync.Map` of
  `*fileState`). Numbering resumes correctly across process restarts by scanning disk
  (`scanLatestVersion`) the first time a fileID is touched.
- Explicitly documented as NOT touching the catalog's CurrentVersion pointer or WAL —
  that wiring is this subtask.
- `writeVersionFile` uses temp-file + rename for durability (already fsync'd before
  return), so by the time `WriteVersion` returns, the version file is durable — this is
  the "post-durable-write" precondition the CAS must be sequenced after.

## engine/catalog/record.go
- `CatalogRecord.CurrentVersion uint64` already exists (confirmed by 2a.1.1's verifier)
  — the field this subtask's CAS targets. `Encode`/`Decode` round-trip it as part of the
  fixed-size record layout; no schema change needed.

## engine/catalog/catalog.go
- `Catalog` has striped mutexes (`stripes [numStripes]sync.Mutex`, keyed by
  `stripeFor(fileID)`) plus a separate `indexMu` guarding the fileID->location map, plus
  per-page `pageStripes`. `Put`/`Get`/`Delete` each acquire-then-release the relevant
  stripe lock internally; there is NO exposed way for a caller to hold a fileID's stripe
  lock across a `Get` call and a subsequent conditional `Put` call — those are two
  separate lock acquisitions with a caller-visible gap between them, which would race.
- Conclusion: a caller-side "Get, compare in Go, Put if match" is NOT race-free with
  Catalog's current public API (confirms the task brief's suspicion). A new
  `Catalog.CompareAndSwapCurrentVersion(fileID, expected, newVersion) (bool,
  CatalogRecord, error)` method is required, implemented INSIDE catalog.go so the whole
  read-check-write sequence executes under one held stripe-lock acquisition (mirroring
  Put's existing delete-then-reinsert pattern, reusing `readSlot`/`tombstone`/`insert`
  exactly as Put does).

## docs/LLD/mvcc.md / docs/LLD/catalog.md
- mvcc.md "Write path": "An atomic CAS swaps 'current version' pointer in catalog
  record fileID once new version durably written." Confirms write-then-CAS ordering.
- mvcc.md "Interactions with other modules" also states "every version-pointer
  CAS...therefore goes through WAL first" — WAL wiring is explicitly out of scope here
  (impacted modules are only engine/mvcc/write.go + write_test.go per the task brief);
  noted as a known, intentional gap consistent with 2a.1.1's own deferral, not a
  regression.
- catalog.md: "`mvcc/` performs an atomic CAS on `currentVersion` when a write
  commits" — matches the design implemented.

## Design decision: what "no lost updates" means for N concurrent writers
Because `VersionWriter.WriteVersion` always assigns a BRAND NEW, never-reused version
number per call (even on a retry after a losing CAS — the task brief explicitly directs
"write a NEW version file with a fresh N reflecting the current state"), the numeric
final CurrentVersion after N concurrent commits is NOT necessarily N: retries consume
extra version numbers for their orphaned (never-referenced) attempts. The precise,
checkable invariant (documented in `CommitVersion`'s doc comment and asserted in the
test) is: the final CurrentVersion equals the HIGHEST version-file number that exists on
disk for that fileID once all N calls have returned, and every one of the N calls'
distinct data payloads is durably captured as some retained version file — none is ever
silently dropped.
