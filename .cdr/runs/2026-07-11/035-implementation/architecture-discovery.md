# Architecture Discovery — Subtask 4.5.11.1

## Index-first discovery trail (in required order)
1. `docs/LLD/graph.md` — describes `graph.dat` CSR format, per-node edge log
   (`edgelog.go`), and `Compact` (`compact.go`)'s crash-safety/retry-idempotency
   design, including the compact-state sidecar and "segment-number reuse" fix.
2. `.cdr/index/regression.jsonl` — entry `run: .cdr/runs/2026-07-08/010-verification`,
   subtask 3.1.3, severity critical, `resolved: false`. This is the exact finding
   (F2) issue #49's subtask 4.5.11.1 is built from: sidecar records "compacted
   through segment N"; `TruncateNode` used to delete-and-recreate the node's log
   directory; `wal.OpenWriter` restarts numbering at 0 for a fresh directory; the
   next `Compact` then treats the reused segment 0 as already-covered by the
   stale sidecar entry and silently skips it, losing the edge.
3. `engine/graph/compact.go`, `engine/graph/edgelog.go` (full read) — see finding
   below: the fix described by 010-verification's recommendation is **already
   implemented in code**, via git commit `ed57468` ("fix: prevent WAL
   segment-number reuse from silently dropping edges after compaction (issue #15,
   3.1.3, second fix cycle)"), dated 2026-07-08, same day as the regression run
   that found it. `ed57468` predates the current `HEAD` (`d38003a`) by many
   commits — it is fully present at `HEAD`.
4. `engine/graph/compact_test.go` — already contains
   `TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost` (added by
   `ed57468`), which exercises exactly the acceptance-criteria scenario
   (append/compact/append/compact, asserting `Weight=8`), plus a third
   append+compact round confirming segment numbering keeps advancing rather than
   colliding again. All of these pass at `HEAD` (verified via
   `go test ./engine/graph/... -race -v`, all green, see self-consistency.json).

## Key finding: fix already landed; the literal acceptance-criteria mechanism differs from the issue text, but achieves the same guarantee
The issue's acceptance criteria says the sidecar entry should be "invalidated/
cleared" on successful truncation. The fix actually implemented (`ed57468`) takes
a different, and arguably more robust, approach: rather than deleting the node's
edge-log directory (which is what causes `wal.OpenWriter`'s segment numbering to
reset to 0 and become reusable), `EdgeLog.TruncateNode` (`engine/graph/edgelog.go`
lines 240-291) no longer removes the directory at all. Instead, before removing
any segment file, it durably writes a monotonic "segment floor" marker
(`wal.WriteSegmentFloor`, `engine/wal/writer.go`) one past the highest segment
number ever used in that directory. `wal.OpenWriter`'s `latestSegmentNum`
(`engine/wal/writer.go` lines 176-231) honors this floor on the next open, so
segment numbers for a given node's log are **never reused** across the node's
entire lifetime — which makes the compact-state sidecar's "compacted-through
segment N" entries permanently valid (monotonically increasing keys), so there is
no longer any need to invalidate/clear them at all. This closes exactly the
class of bug the acceptance criteria describes ("next Compact() never mistakes a
reused segment number for one it already processed") without needing the
literal clear/invalidate mechanism the issue text proposed — a stronger fix
(never reuse) subsumes a weaker one (clear stale state after reuse), and this
was already independently verified in a prior fix-cycle documented at length in
`compact.go`'s and `edgelog.go`'s own package/method doc comments ("Segment-number
reuse (second fix cycle)").

## What remains for this run
The **code fix is already present and already covered by an equivalent test**
(`TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost`). However, issue
#49's subtask 4.5.11.1 explicitly names a required test:
`TestCompactNormalTruncateThenAppendThenCompactAgain`, run via
`go test ./engine/graph/... -run TestCompactNormalTruncateThenAppendThenCompactAgain`.
That exact-named test does not exist in `compact_test.go` today. This run's
concrete deliverable is therefore to add that exact-named regression test (not a
mechanical duplicate copy-paste, but written directly against the issue's own
worked example: `AppendEdge(weight=3)` -> `Compact` -> `AppendEdge(weight=5)` ->
`Compact` -> assert `Weight=8` and non-empty edge log state), so that:
  - the issue's literal `go test -run <name>` acceptance command passes,
  - CI/verification tooling that greps for this exact test name finds it,
  - the regression is pinned by name to the exact issue/subtask that reported it,
    independent of `TestCompaction_SecondAppendAfterSuccessfulCompactionIsNotLost`
    (which stays as-is, unmodified, per the "smallest diff" principle — it is not
    superseded, just not the literally-named acceptance test).

No production code change is required or made in this run: `compact.go` and
`edgelog.go` are read-only reference points for this run, confirmed already
correct. Only `compact_test.go` is modified (test-only diff).
