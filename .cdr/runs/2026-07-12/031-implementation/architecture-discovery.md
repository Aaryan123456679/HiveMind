# Architecture discovery — Subtask 7.3.1

## Token order followed

1. `.cdr/index/{task,feature,file,regression}.jsonl` (grep for 6.3.1-6.3.4/loadtest/soak).
2. `.cdr/memory/state.md`, `.cdr/memory/pending.md` etc. — no prior soak-test entries found.
3. Prior implementation/verification handoffs for 6.3.1-6.3.4 (via index pointers).
4. Targeted LLD docs: `docs/LLD/eval.md`, `docs/LLD/mvcc.md`, `docs/LLD/catalog.md`,
   `docs/LLD/wal.md`, `docs/HLD.md` sections 3/3.1/5/7.
5. Touched-file precedents: `engine/loadtest/harness.go`, `ingestion_bench_test.go`,
   `query_latency_test.go`, `split_race_scale_test.go` (all read in full before any
   new source was written).

## Key findings

- `docs/LLD/eval.md` ("`engine/loadtest/` storage-engine concurrency benchmark")
  documents the harness's three existing uses (ingestion throughput, query latency
  under ingestion load, auto-split race) and states "concurrency tests gated
  `go test -race`" (per `AGENT.md`) — the soak test must follow the same race-gating
  convention.
- `docs/LLD/mvcc.md` "Read path" / "Known risks" sections describe the
  no-reader-blocking guarantee `query_latency_test.go` already exercises; the soak
  test reuses the exact same fixture shape (real `catalog.Catalog` + `wal.Writer` +
  `mvcc.VersionWriter` + `mvcc.EpochManager`, `mvcc.CommitVersion` /
  `mvcc.SnapshotRead`) rather than inventing a new storage seam, since sustained
  concurrent ingestion+query is precisely what that stack is for.
- `docs/LLD/catalog.md` "Concurrency" / striped-locking sections and `docs/LLD/wal.md`
  "Invariant" section confirm WAL-before-apply and per-fileID striped locking are the
  concurrency primitives already proven correct by `catalog`/`mvcc`'s own test
  suites; the soak test's job is sustained-duration stress of this same path, not a
  new correctness proof.
- `engine/loadtest/harness.go`'s `Run()` is iteration-count-based (`Workers x
  Iterations`, fixed total), not duration-based — it has no "run until wall-clock
  deadline" mode. A soak test needs an open-ended "keep going until time X" loop
  shape. Per this subtask's non-goals (see requirement.md), the harness's public API
  is NOT changed; the soak test instead drives its own `sync.WaitGroup` +
  `atomic` counters loop, structurally identical in spirit to
  `split_race_scale_test.go`'s precedent of hand-rolling fixture/driver code when an
  existing exported surface doesn't fit a new scenario shape, while still reusing
  `harness.go`'s existing `Run`-based tests' underlying real-engine fixtures
  (`catalog.Open`, `wal.OpenWriter`, `mvcc.NewVersionWriter`, `mvcc.NewEpochManager`)
  verbatim.
- 6.3.4's package doc comment establishes the precedent this subtask explicitly
  follows for honest duration-scaling: measure real per-op cost, then pick a bounded,
  CI-viable duration/workload and disclose the scaling rationale in a doc comment
  rather than pretending the scaled-down run is equivalent to the literal spec
  ("hours").
- No existing `TestSoak` or soak-test artifact found anywhere in `.cdr/index/` or
  `engine/loadtest/` — this is a wholly new test, no regression risk to prior tests
  from editing shared code (only a new file is added).

## Interactions / modules touched

- New file only: `engine/loadtest/soak_test.go`.
- Depends on (unchanged, read-only): `engine/catalog`, `engine/wal`, `engine/mvcc`.
- Does not touch: `engine/loadtest/harness.go`, `ingestion_bench_test.go`,
  `query_latency_test.go`, `split_race_scale_test.go`, or any non-test engine source
  — unless the real soak run in step 6 surfaces an actual bug, in which case the
  minimal fix will be scoped and documented separately (see plan.md).
