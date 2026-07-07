1. Add `wal.RecordSplitCommit` + payload encode/decode, mirroring existing
   CatalogPut/BTreeInsert payload conventions.
2. Register the new record type in `wal.isValidRecordType`.
3. Add `graph.EdgeAppender.AppendEdgeIfAbsent` (idempotent append) — the one
   new primitive graph needs to be replay-safe.
4. Implement `split.ExecuteSplitAtomic`: precondition StatusSplitting,
   allocate+write new content (reuse `ExecuteSplitAllocateAndWrite`), sort
   new paths canonically, build the redirect stub, build the updated
   catalog record, append `RecordSplitCommit` via `wal.AppendAndApply` with
   an apply closure that does catalog Put + btree inserts + graph edges,
   instrumented with 4 test-only hook stages; release `FileGuard` only on
   full success.
5. Implement `split.RecoverSplitCommits`: replay pass over the WAL dir,
   skip non-matching record types, re-apply catalog+btree+graph for each
   matching record (idempotently).
6. Extend `execute_test.go` with `TestSplitAtomicCommit`: happy path,
   nil/precondition checks, 4 crash-point subtests each followed by
   recovery + a second recovery to check idempotency, and
   `RecoverSplitCommits`-specific nil/empty-dir checks.
7. Self-consistency: build/vet/fmt clean; targeted test green; full
   `-race` regression for split/wal/graph; full module `go test ./...`.
8. One local commit, no push. Write handoff.json.
