# Requirement — Subtask 1.3.1

Source: GitHub issue #3, Epic "Phase 1: Storage core", checklist item 1.3.1
(verbatim, confirmed via `gh issue view 3`):

- **1.3.1 — Append-only WAL segment writer + segment rotation**
  - Acceptance criteria: Log records append to `wal/wal-<segment>.log`; a new
    segment is created once the current one exceeds a configured size, with
    no record ever split across segments.
  - Test spec: `go test ./engine/wal/... -run TestSegmentWriter -race`: write
    enough records to force rotation, assert segment boundaries and record
    integrity.
  - Impacted modules: `engine/wal/writer.go, engine/wal/writer_test.go`

## Scope boundary for this subtask (per issue decomposition)

This subtask is ONLY the segment writer mechanics: on-disk record framing,
append, and size-based rotation. The following are explicitly out of scope
and deferred to later subtasks in the same epic:

- 1.3.2: WAL record *types* for catalog/index mutations and the
  fsync-before-apply write-path contract (this subtask still fsyncs per
  append for basic durability discipline, but does not define record
  semantics/types).
- 1.3.3: checkpoint manifest (`manifest.json`).
- 1.3.4: recovery replay from checkpoint pointer.
- 1.3.5: crash-injection / torn-record recovery handling.

Resuming an existing WAL directory (scanning for the highest existing
segment number on `OpenWriter`) is *supported* here for correctness (a
writer must not clobber existing segments), but full crash-recovery /
torn-tail detection is explicitly out of scope (1.3.4/1.3.5).
