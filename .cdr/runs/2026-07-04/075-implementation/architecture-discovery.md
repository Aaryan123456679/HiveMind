# Architecture discovery — 2a.1.4

## Read (index-first, then targeted source)
- .cdr/index/task.jsonl: 2a.1.1/2a.1.2/2a.1.3 verified. 2a.1.2's verification
  handoff explicitly flagged: "Consider asserting ... CommitVersion must not be
  wired into any caller until 2a.1.4's WAL-before-CAS wiring lands."
- engine/mvcc/write.go: CommitVersion(cat, fileID, data) currently loops:
  cat.Get -> expected := rec.CurrentVersion -> WriteVersion (durable, own
  per-fileID numbering lock) -> cat.CompareAndSwapCurrentVersion(fileID,
  expected, version) directly, no WAL involved at all today.
- engine/catalog/catalog.go: CompareAndSwapCurrentVersion acquires the
  fileID's stripe lock, re-reads the record, checks CurrentVersion==expected,
  and if so tombstones the old slot + inserts a new encoded record + updates
  the index — all in-memory/on-disk page mutation, NO WAL logging. It is the
  only path (documented) that mutates CurrentVersion; ordinary Catalog.Put
  also exists but by convention nothing but CommitVersion touches
  CurrentVersion in the version-pointer flow.
- engine/catalog/content.go: Create/Append already implement WAL-before-apply
  via wal.AppendAndApply + wal.NewCatalogPutRecord, wrapping the actual
  cat.Put call as the "apply" callback. This is the pattern subtask 2a.1.4 is
  told to mirror.
- engine/wal/record.go: RecordCatalogPut/CatalogPutPayload carries an
  already-Encode()'d CatalogRecord blob keyed by FileID; AppendAndApply
  fsyncs the WAL record before invoking apply (structural guarantee, not
  convention).
- engine/catalog/recovery.go: RecoverFromWAL replays RecordCatalogPut records
  in on-disk order via cat.Put UNCONDITIONALLY (no CAS check on replay) — "last
  Put wins". This means WHATEVER gets logged as a CatalogPut record for a
  fileID, if durable, WILL become that fileID's state after a crash+replay,
  regardless of what the live in-memory CAS decided.
- docs/LLD/mvcc.md: "An atomic CAS swaps the 'current version' pointer in the
  catalog record for fileID once the new version is durably written" /
  "every version-pointer CAS is a catalog mutation and therefore goes through
  the WAL first".
- docs/LLD/wal.md: "every mutation to the catalog or any index must be logged
  to the WAL before it is applied in memory or on disk."

## Key design tension found (not present in Create/Append's simpler case)
Create/Append's WAL-then-apply pattern is safe to reuse verbatim ONLY because
their "apply" (cat.Put) is unconditional — it always succeeds once WAL is
durable, so whatever gets logged always matches what gets applied.
CompareAndSwapCurrentVersion is NOT unconditional: it can lose a race (CAS
refused) if some other CommitVersion call's CAS already advanced
CurrentVersion between when this call captured `expected` and when it
attempts to apply. If we naively wrap the existing lost-race-and-retry
CommitVersion loop's CAS call as `apply` inside wal.AppendAndApply exactly
like Create/Append do, a "doomed" (losing) CAS attempt would still get its
WAL record durably written BEFORE we know it will lose — and because
RecoverFromWAL replays CatalogPut records unconditionally (no CAS
re-validation on replay), a crash landing exactly between that "doomed"
record's durable append and the live retry loop's next (winning) attempt
could leave recovery reconstructing the LOSING/stale CurrentVersion instead
of the actually-intended final value, silently violating "recovery must
reconstruct a valid, consistent CurrentVersion."

## Resolution (see plan.md for full justification)
Reuse RecordCatalogPut/CatalogPutPayload exactly as-is (no new WAL record
type: recovery only ever needs "this fileID's CatalogRecord became this" —
a version-pointer CAS is, at rest, indistinguishable from any other catalog
Put once durable, matching mvcc.md's own framing of it as "a catalog
mutation"). To close the doomed-record gap without touching
engine/catalog/catalog.go's CompareAndSwapCurrentVersion signature (keeping
the change scoped to engine/mvcc/write.go per the subtask's stated impacted
modules), introduce a per-fileID commit lock INSIDE VersionWriter that
serializes the "verify expected still holds -> WAL-log -> apply CAS" critical
section for a given fileID. Because CompareAndSwapCurrentVersion is only ever
invoked through this now-serialized path, by the time a goroutine decides to
WAL-log (having just re-verified expected == current, holding the lock the
whole time through the WAL append and the eventual CompareAndSwapCurrentVersion
call inside apply), no other goroutine can have raced it: the CAS inside apply
is therefore guaranteed to succeed, and nothing is ever logged for an attempt
that will not actually be applied. A losing attempt is now detected BEFORE any
WAL write (re-check under the commit lock returns "stale", nothing logged),
and the outer CommitVersion loop retries exactly as before (fresh
WriteVersion, fresh attempt) — preserving 2a.1.2's documented "no lost
updates, orphaned losing version files" contract and its existing test
(TestCurrentVersionCAS), which only asserts fileCount >= numGoroutines.

Consequence: CommitVersion's signature gains a `*wal.Writer` parameter
(matching the per-call-injection style already used for `cat`, rather than
storing it on VersionWriter). This is a necessary, expected breaking change —
2a.1.2's own verification handoff anticipated CommitVersion's WAL wiring
landing in this subtask. Existing callers (engine/mvcc/write_test.go,
engine/mvcc/read_test.go) are updated to open a wal.Writer alongside the test
catalog, mirroring engine/catalog/content_test.go's newTestContentStore
pattern.
