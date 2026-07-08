# Requirement (F2 fix cycle, second CHANGES_REQUESTED)

Source: `.cdr/runs/2026-07-08/010-verification/verification.json`, blocking finding F2.

F1 (fixed in commit 9850083, confirmed genuine via independent revert-experiment in
010-verification) added a durable per-node "compacted-through segment number" sidecar
file to stop compaction retries from double-counting weight after a failed TruncateNode.

That fix introduced F2: `EdgeLog.TruncateNode` fully removed a node's per-node log
directory on every successful truncation. `wal.OpenWriter`'s `latestSegmentNum` always
restarts numbering at 0 for an empty/new directory. So the very next edge appended to
the same node after ANY ordinary, fully-successful compaction+truncation would silently
reuse a segment number the compact-state sidecar had already recorded as "already
folded into graph.dat" - causing the next Compact to skip it as already-reflected,
permanently and silently losing it. No crash or failure injection required; two
ordinary Compact cycles on the same node reproduce it deterministically.

Repro (from 010-verification):
1. AppendEdge(weight=3) on node X -> Compact() fully succeeds (no failure injected).
2. AppendEdge(weight=5) on the SAME node X.
3. Compact() again (ordinary next cycle).
4. LoadCSR shows Weight=3, not 8 - the second edge is silently and permanently lost.

## Required regression tests
1. F2's exact scenario: append, compact (full success), append again to the same node,
   compact again, assert both edges are reflected (weight=8).
2. F1's existing regression test must still pass unmodified in spirit.
3. A combined test: failed-truncate retry cycle (F1) followed by normal subsequent
   appends+compactions (F2) on the same node, to catch any interaction bug at the seam.

## Fix direction evaluated and chosen
Two candidate approaches were evaluated per the dispatch instructions:
(a) clear the sidecar entry for a node as part of/immediately after successful
    truncation, vs.
(b) never reuse segment numbers after a truncation (persist a "next segment number"
    floor instead of deleting the directory and letting wal.OpenWriter restart at 0).

Chosen: (b). See compact.go's and edgelog.go's updated doc comments for the full
trace-through showing (a)'s crash-window would reproduce F2's exact severity if the
process crashes between TruncateNode succeeding and the sidecar-clear write, whereas
(b) is self-sufficient (does not require any sidecar-clear coordination at all) and its
own crash window (a crash between writing the floor and finishing segment-file removal)
degrades safely to the already-accepted, already-tested F1 retry scenario rather than to
F2's silent-data-loss scenario.
