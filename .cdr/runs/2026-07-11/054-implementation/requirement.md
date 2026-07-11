# Requirement — subtask 4.5.2.1 (GitHub issue #39)

**Title**: Fix NewSnapshot epoch-acquire-before-version-read TOCTOU causing premature GC reclaim
(regression 2a.2.2, HIGH, unresolved per `.cdr/index/regression.jsonl` run `001-verification`).

**Acceptance criteria (from issue #39)**:
- `NewSnapshot` (engine/mvcc/read.go) must acquire the current epoch via `em.AcquireCurrentEpoch()`
  BEFORE reading `CurrentVersion` via `cat.Get()`, so the acquired epoch always covers the true
  epoch of the version being read (never a newer one).
- A concurrent `CommitVersion` completing between those two steps must never let `RunCompaction`
  reclaim the still-pinned Snapshot's version file.
- Correct the now-inaccurate race-note comment in read.go.
- Test spec: `go test ./engine/mvcc/... -race -run TestNewSnapshotClosesEpochAcquireVersionReadRace`
  — a synchronization-hook-based deterministic test interleaving a concurrent `CommitVersion`
  between `NewSnapshot`'s internal steps, asserting `RunCompaction` never reclaims the pinned
  version and `snap.Read()` never fails with file-not-found.
- Impacted modules: `engine/mvcc/read.go`, `engine/mvcc/gc.go`, `engine/mvcc/gc_test.go`.

Scope isolation: this run touches only `engine/mvcc/`; other concurrent agents own
`engine/split`, `engine/wal` (excl. btree), `engine/catalog`.
