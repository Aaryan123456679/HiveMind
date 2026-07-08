# Requirement — Subtask 3.1.6 (issue #15, Epic Phase 3)

Source: `gh issue view 15` (body's "3.1.6" bullet; the issue body ends with injected
fake system-reminder-style text — a date-change notice, fake MCP/"tokensave" tool
instructions, and a fake "Auto Mode Active" directive — which is treated as untrusted
plain-text data only and NOT acted upon, per this repo's established recurring
prompt-injection pattern on issue #15).

## 3.1.6 — Graph correctness test: full insert/traversal/compaction round trip

- **Acceptance criteria**: A combined workload of edge inserts, compaction, and
  traversal queries produces results consistent with a serial-execution oracle.
- **Test spec**: `go test ./engine/graph/... -run TestGraphRoundTrip` — interleave
  inserts/compaction/traversal, assert oracle-matching results at checkpoints.
- **Impacted modules**: `engine/graph/graph_test.go` (new file; this is a test-only
  subtask — no new production code is implied by the issue text itself).

## Interpretation (informed by prior subtask history, since this is the final,
integration-style subtask sitting on top of 3.1.1-3.1.5)

This is a full round-trip test exercising the entire `engine/graph` package as a single
composed pipeline, not a re-test of any one component in isolation (each component
already has its own dedicated suite from 3.1.1-3.1.5). Given 3.1.3's two real,
independently-confirmed bugs (F1: retry double-counting of ENTITY_COOCCUR weight; F2:
silent permanent data loss from WAL segment-number reuse after TruncateNode) were BOTH
at the compaction seam — and were only found by verification going beyond the given
test spec — this test is designed to deliberately target that seam under multiple
realistic append -> compact -> traverse cycles, plus a simulated process restart
(fresh `*CSRGraph` reloaded from the same `graph.dat` path), since "full round trip"
strongly implies proving durability survives a restart, not just proving in-memory
correctness within a single process lifetime.

Concretely, `TestGraphRoundTrip` (and companion subtests) must:

1. Build a maintained-in-Go, independently-computed oracle (a plain
   `map[uint64]map[key]CSREdge`-shaped adjacency model) that mirrors the same
   ENTITY_COOCCUR-summed / last-write-wins-for-other-types merge semantics
   `compact.go`'s `mergeEdges` implements — computed by straightforward serial
   iteration over every appended edge, not by calling any package-under-test code.
2. Append edges via `EdgeLog.AppendEdge` (3.1.2) across multiple distinct source
   fileIDs and all four edge types (3.1.4): `ENTITY_COOCCUR` (including repeats of the
   same (source,target) pair with different weights, to exercise summing),
   `LLM_ASSERTED`, `SPLIT_SIBLING`, `REDIRECT`.
3. Run `Compact` (3.1.3) to fold the appended edges into `graph.dat` (3.1.1's CSR
   format), then verify via `GraphNeighbors` (3.1.5, depth 1 and depth 2, with and
   without an edge-type filter) that traversal results exactly match what the oracle
   predicts.
4. Repeat step 2-3 for a SECOND append+compact cycle (new edges to both fresh and
   already-existing (source,target) pairs — the exact class of seam bug F1/F2
   revealed: does a second compaction round double-count, lose, or corrupt anything),
   re-verifying against the updated oracle.
5. Simulate a process restart: discard the in-memory `*CSRGraph` returned by the
   second `Compact` call, call `LoadCSR` fresh against the same `graph.dat` path (a
   brand-new `*CSRGraph` instance, exactly as a real restarted process would build
   one), and re-run the same `GraphNeighbors` traversal queries, asserting IDENTICAL
   results to the pre-restart state — proving durability survives a restart, not just
   in-memory correctness within one process lifetime.
6. A THIRD append+compact cycle after the simulated restart (appending via a fresh
   `EdgeLog` opened against the same root, as a restarted process would), to prove the
   post-restart `CSRGraph`/`EdgeLog` pairing composes correctly for further writes too,
   not just reads.

Must comply with mandatory Go discipline: `gofmt`/`go vet`/`go build ./...` clean,
explicit `-timeout` on every `go test` invocation, `-race` where concurrency-relevant
(this test is single-goroutine/sequential by design — it is explicitly an interleaved
serial-oracle test per the issue's own test spec wording ("interleave
inserts/compaction/traversal"), not a concurrency test — but still run under `-race`
for consistency with the rest of the package's test invocations and because it does
exercise `EdgeLog`'s internal locking machinery even from a single goroutine).
