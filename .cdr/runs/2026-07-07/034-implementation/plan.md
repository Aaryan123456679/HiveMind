# Plan — issue #14

## Step A — fix the real bug (new-fileID catalog records)
1. `engine/wal/record.go`: `SplitCommitEntry` gains `SizeBytes uint64`; `Encode`
   appends 8 LE bytes per entry after FileID; `Decode` reads them back
   symmetrically.
2. `engine/wal/record_test.go`: add non-zero `SizeBytes` to the `SplitCommit`
   subtest's `want.Entries`.
3. `engine/split/execute.go`:
   - `ExecuteSplitAtomic`: after allocating `newFileIDsByPath`, compute
     `newPathSizes[newPath] = len(extractSections(originalContent, proposal.SectionRanges))`
     for each `proposal` in `plan.Files`; set `entries[i].SizeBytes` accordingly.
     Inside the apply closure, right after `cat.Put(updated)` succeeds, loop
     over `entries` and `cat.Put` a fresh `catalog.CatalogRecord{FileID,
     CurrentVersion: 0, SizeBytes, Status: catalog.StatusActive}` for each.
   - `RecoverSplitCommits`: mirror the same loop using `payload.Entries`
     (now carrying `SizeBytes`), right after its own `cat.Put(updated)`.
4. `engine/split/execute_test.go`: extend `assertFullSplitApplied` with
   `cat.Get(fileID)` + Status/SizeBytes assertions for every new fileID.
5. Run `go test ./wal/... ./split/... ./catalog/... -race -count=1` to confirm
   the fix and zero regressions in already-verified 2b.3.x/2b.4.1 tests.

## Step B — 2b.5.1: TestConcurrentAppendSplitRace
- New file `engine/split/split_race_test.go`.
- Harness: single fileID, `catalog.ContentStore` (real, tmp-dir backed),
  `Orchestrator`, `FileGuard`, `btree.Tree`, `graph.EdgeAppender` (tracked, for
  `graph.ReadAll`), `catalog.IDAllocator`, shared `wal.Writer`.
- N writer goroutines each append M uniquely-tagged chunks (goroutine index +
  sequence number encoded in the payload) to the same fileID, looping:
  `AdmitWrite` -> if `ErrSplitInProgress`, brief backoff+retry; else `Append`;
  if `Append` returns `thresholdCrossed==true`, THIS goroutine becomes the
  split driver for this round: `BeginSplit` -> read current content via
  `cs.Read` -> build a 2-way `SplitPlan` (split the current content
  byte-for-byte in half, no data loss) -> `ExecuteSplitAtomic`. On any error
  from BeginSplit/ExecuteSplitAtomic that isn't a legitimate "someone else already
  split" race, fail the test via a shared error channel/atomic flag (not
  `t.Fatalf` directly from a goroutine).
- Track globally (atomically) every chunk successfully appended, and, once a
  split completes, redirect subsequent rounds' target fileID to (one of) the
  new files so later chunks continue landing somewhere real -- run several
  such rounds (multiple threshold crossings in aggregate, matching the issue's
  acceptance criteria) sized similarly to 2a.4.5's rigor (~30-40 goroutines,
  several thousand small appends across rounds).
- After all goroutines finish: assert (a) no data loss -- every chunk's unique
  tag is found exactly once across the final reachable file set (walk B+Tree
  from the redirect-stub chain via `RedirectTargetIDs`, `cs.Read` each leaf,
  concatenate/collect all tags); (b) exactly one split occurred per threshold
  crossing -- a split-count counter incremented only by the one goroutine per
  round that wins `BeginSplit` (guard CAS), cross-checked against the number of
  StatusRedirect catalog records produced; (c) no dangling graph edges -- every
  edge read back via `graph.ReadAll` has both Source and Target resolvable via
  `cat.Get` (this is exactly the invariant the Step-A fix restores: without the
  fix, EVERY split-off file is graph-referenced but catalog-orphaned, i.e.
  "dangling" under this definition).
- Run under `-race`, `go test ./split/... -race -run TestConcurrentAppendSplitRace -count=10` per the issue's test spec.

## Step C — 2b.5.2: TestReaderDuringSplit
- Fresh fileID with meaningful content; take a real `mvcc.Snapshot` (via
  `engine/mvcc`) immediately before driving a split (`BeginSplit` ->
  `ExecuteSplitAtomic`) concurrently on another goroutine.
- Long-running reader goroutine repeatedly reads the pre-split snapshot's
  pinned version throughout the split window (via `mvcc.Snapshot.Read`, NOT
  `cs.Read(fileID)`, since after the split fileID's content changes to the
  redirect stub -- exactly the isolation `orchestrate_test.go`'s
  `TestSplittingStatusIsolation` already exercises for the SPLITTING window
  alone; this subtask extends it across a REAL `ExecuteSplitAtomic` call, not
  just the Status transition) and asserts every read returns byte-identical
  pre-split content.
- Run under `-race`, `go test ./split/... -race -run TestReaderDuringSplit`.

## Step D — self-consistency, commit, handoff
- `go build ./... && go vet ./... && gofmt -l .` clean.
- `go test ./split/... -race -count=1 -timeout 20m` and full
  `go test ./... -count=1 -timeout 25m` clean.
- One commit (or two if the bugfix is cleanly separable and matches repo
  convention -- decide at commit time; likely ONE combined commit since this is
  a single-agent pass finding+fixing its own bug, per 2b.4.1's precedent
  option).
- `handoff.json` with pointers only.
