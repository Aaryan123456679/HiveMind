# Plan

## Chosen approach: durable per-node "compacted-through segment number" marker

Evaluated options:

1. **Per-node compacted-through marker (chosen).** A small sidecar file next
   to `graph.dat` (`graph.dat.compact-state`) records, per source fileID, the
   highest edge-log segment number already durably folded into `graph.dat`.
   On each `Compact()` call, segments at or below that number are skipped
   when computing what counts as "incoming" for a node - they're already
   inside `existing` (loaded from `graph.dat`). Written atomically (temp +
   fsync + rename, matching `csr.go`'s `WriteCSR`) immediately after
   `WriteCSR`'s own rename succeeds, before any `TruncateNode` call.
2. **Marker embedded inside `graph.dat` itself** (single-rename atomicity,
   closing even the residual two-rename crash window). Rejected: requires
   changing `csr.go`'s on-disk format and `LoadCSR`'s strict payload-length
   validation, which other `LoadCSR` callers depend on staying exactly as
   task-3.1.1 defined it - disproportionate blast radius for this fix.
3. **Per-segment truncation immediately after each segment's entries are
   written** (truncate incrementally rather than one bulk pass at the end).
   Rejected on its own: `Compact()`'s core design (per task-3.1.1/3.1.2) is a
   full-snapshot rebuild - `graph.dat` is written *once*, as a whole, only
   after *all* nodes' data has already been merged into one in-memory
   `adjacency` map and `BuildCSR`'d. There is no point at which "this
   segment's entries are durably reflected in graph.dat" for one node without
   the entire snapshot having already been written for every node - so
   per-segment truncation could only run after the single `WriteCSR` call
   anyway, giving no crash-safety improvement over truncating everything
   after that single write (which is what the pre-fix code already did). The
   segment-number bookkeeping this fix needed *is* still consumed
   per-segment (via `ReadNodeAfter`), just not truncated per-segment.
4. **Staging/move-then-delete edge-log segments before writing graph.dat.**
   Considered and rejected: moving segments out of their normal location
   before `WriteCSR` succeeds would require `ReadNode`/`edgeLogNodeIDs` to
   also search the staging location to preserve the crash-before-rename
   invariant ("logs left completely untouched" - `TestCompaction_
   CrashBeforeRenameLeavesOldGraphAndLogsIntact` checks this via `ReadNode`),
   materially increasing surface area and risk for no benefit over option 1.

Option 1 was picked as "simplest and most robust": it reuses the existing
`wal-<N>.log` segment-numbering scheme already present in `edgelog.go`
(`listWALSegments` already parsed and then discarded these numbers), needs no
change to `csr.go`, and closes the *entire* class of failure this package's
own crash-injection tests exercise (deterministic operation failures, not
literal process-crash-mid-fsync) - see `compact.go`'s new "Retry idempotency"
doc comment section for the honestly-disclosed residual (a real crash in the
sub-millisecond window between `WriteCSR`'s rename and the sidecar's own
rename), which is qualitatively narrower and non-compounding compared to the
bug being fixed.

## Steps

1. `edgelog.go`: expose segment numbers (`numberedSegment` promoted to
   package scope, new `listWALSegmentsNumbered`); add
   `EdgeLog.ReadNodeAfter(id, afterSeg) ([]CSREdge, maxSeg int, err error)`;
   reimplement `ReadNode` in terms of it (`afterSeg = -1`).
2. `compact.go`: add `compactStatePath`/`loadCompactState`/`saveCompactState`
   (magic+version+count+CRC framed, atomic temp+fsync+rename, mirroring
   `csr.go`'s own `WriteCSR`/`LoadCSR`). Rewrite `Compact()`: load prior
   state, use `ReadNodeAfter` per node instead of `ReadNode`, write
   `graph.dat` (unchanged), then persist the new state (after `WriteCSR`,
   before any `TruncateNode`), then truncate every node that had any segment
   on disk this round (self-healing cleanup of prior leftover segments too).
3. `compact_test.go`: add
   `TestCompaction_RetryAfterTruncateFailureDoesNotDoubleCountWeight`,
   reusing `TestCompaction_TruncateFailureDoesNotLoseGraphUpdate`'s exact
   setup, then performing a second `Compact()` call after lifting the
   failure and asserting the weight is unchanged (and the log is now fully
   truncated).
4. Verify existing tests untouched and passing; verify the new test fails
   against the pre-fix code (empirically confirmed: Weight=6 vs want 3) and
   passes against the fix.
