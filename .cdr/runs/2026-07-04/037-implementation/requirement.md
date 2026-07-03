# Requirement — Subtask 1.3.2

Source: GitHub issue #3 ("[1] WAL + crash recovery (engine/wal/)"), Epic "Phase 1: Storage core
(single-threaded)", checklist item 1.3.2 (verbatim from `gh issue view 3`):

- [ ] **1.3.2 — WAL record types for catalog/index mutations + fsync-before-apply write path**
  - Acceptance criteria: A mutation is durably fsynced to the WAL before the caller's Append call
    returns, and before the corresponding in-memory/on-disk state change is considered committed.
  - Test spec: `go test ./engine/wal/... -run TestFsyncBeforeApply -race`: assert WAL append
    completes (and fsync observed) prior to a simulated apply callback firing.
  - Impacted modules: `engine/wal/record.go, engine/wal/record_test.go`

Prior subtask 1.3.1 (append-only WAL segment writer + rotation) is `state: verified` in
`.cdr/index/task.jsonl` (`task-1.3.1`, commit `1a12643cfadd40610afa690d32672e6c59928870`,
verification `PASS_WITH_COMMENTS`). `docs/LLD/wal.md` is explicitly scaffold-only per that
verification and does not yet describe record types — this subtask is the first to define them.

This subtask must NOT re-implement segment writing/rotation/fsync (`Writer.Append` from 1.3.1
already does this and is out of scope to change). It must add a typed-record layer on top that:

1. Defines minimal, typed WAL record kinds sufficient to redo the two mutation families the WAL
   exists to protect: catalog record mutations (`engine/catalog`) and B+Tree index mutations
   (`engine/btree`) — traceable forward to what 1.3.4's recovery replay will need.
2. Provides an `Append`/`AppendAndApply`-style entry point that encodes a typed record and calls
   `Writer.Append` (1.3.1), and — per the acceptance criteria and test spec's literal wording —
   structurally enforces (not just documents) that any "apply" (in-memory/on-disk state mutation)
   happens only after the WAL append (and its fsync) has completed.
3. Ships an encode/decode round trip for each record kind, plus `TestFsyncBeforeApply` proving the
   ordering empirically via an instrumented/observable event sequence, not by code inspection.
4. Documents what happens if the `apply` callback itself errors after a successful durable append
   (the append already durably persisted the intent; that's correct WAL semantics since recovery
   replay is the second-chance mechanism for a failed apply).
