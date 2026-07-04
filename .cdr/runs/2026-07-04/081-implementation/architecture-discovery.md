# Architecture Discovery — task-2a.2.1

## Index-first
- `.cdr/index/task.jsonl`: task-2a.1.* all verified (018f69a is HEAD). No prior
  task-2a.2.* entries exist yet — this is the first subtask of task-2a.2.
- `docs/LLD/mvcc.md` "Garbage collection" section (already documents the intended design,
  written ahead of implementation in a prior planning pass):
  > Uses reference-counted snapshot epochs — a simplified Postgres-vacuum-style
  > visibility scheme: each snapshot increments an epoch's refcount on start and
  > decrements on completion; a version is eligible for GC once its epoch's refcount
  > reaches zero and it is not the current version.
  This confirms the refcounting model this subtask must implement, and that "epoch" is
  the vacuum-style visibility concept, not a per-fileID thing — it's a lifecycle/GC-wide
  concern layered independently of any one fileID's version number.

## engine/mvcc/write.go, read.go (2a.1.1-2a.1.5, existing)
- `Snapshot` (read.go) is a small value pinning `{vw, fileID, version}` — captured once
  in `NewSnapshot` from `catalog.Get(fileID).CurrentVersion`. It has NO lifecycle today:
  no `Close()`, no completion signal, no notion of a global "epoch". `Read()` /
  `readWithHook()` just read bytes off disk; nothing here ever needed a completion hook
  because nothing yet reclaims old version files.
- `TestSnapshotRead` (read_test.go) and `TestConcurrentReadersWriters`
  (mvcc_test.go, 2a.1.5) construct `Snapshot`s via `NewSnapshot`/`SnapshotRead` and never
  call any kind of `Close()` — there is no such method to call.
- `VersionWriter.CommitVersion` (write.go) is the only thing that advances a fileID's
  `CurrentVersion` pointer; it has no notion of a global epoch counter either. Nothing in
  2a.1.* increments any global monotonic counter on write.

## Decision: new, separate concept layered on top (NOT touching Snapshot in place)

Two options considered, per the task brief:
1. Extend `Snapshot` in place with an epoch field + `Close()` method (making epoch
   tracking mandatory for every snapshot, including 2a.1.3/2a.1.5's existing call
   sites, which would then need updating to call `Close()`).
2. Introduce a new, additive `EpochManager` type in `gc.go`, decoupled from `Snapshot`,
   that owns global epoch refcounting as a standalone primitive. Wiring `NewSnapshot`
   to actually *call* `EpochManager.Acquire`/`Release` is left to 2a.2.2/2a.2.3 (the
   compactor subtasks), which is where "GC needs this" actually starts to matter.

Chosen: **option 2**. Reasoning:
- This subtask's acceptance criteria and test spec (`TestEpochRefcount`) are entirely
  about the refcounting bookkeeping itself — "open/close overlapping snapshots across
  epochs, assert refcount is correct at every step" — not about proving `NewSnapshot`
  end-to-end triggers it. The refcounting primitive can and should be tested in
  isolation.
- Backward compatibility: 2a.1.3's `TestSnapshotRead` and 2a.1.5's
  `TestConcurrentReadersWriters` never call `Close()` today. Making epoch-tracking
  mandatory inside `NewSnapshot` in this subtask would force those tests to change
  (or silently leak epoch refs forever, since they'd never release), for a subtask
  whose own acceptance criteria says nothing about wiring `NewSnapshot`/`CommitVersion`
  end-to-end. That end-to-end wiring is naturally 2a.2.2/2a.2.3's job (the compactor
  needs `CommitVersion` to bump the epoch and a real `Snapshot.Close()` to release it,
  once there's an actual consumer of `MinReferencedEpoch()`).
  - Prefer to keep 2a.2.1 laser-focused on the refcounting primitive itself, fully unit
    tested, and hand off a clean, documented API surface (`AcquireCurrentEpoch`,
    `Release`, `RefCount`, `AdvanceEpoch`, `MinReferencedEpoch`) for 2a.2.2 to wire up.
  - Tradeoff, documented explicitly: after 2a.2.1 lands, `NewSnapshot`/`CommitVersion`
    do NOT yet call into `EpochManager` — the refcounting exists but is not yet
    load-bearing for real reads/writes. This is intentional and flagged for 2a.2.2 to
    close, per the requirement's own suggestion that this decision needs documenting
    "since this is foundational for the next 2 subtasks" (see plan.md and
    handoff.json).

## Epoch model (see plan.md for full reasoning)
- Global (not per-fileID) monotonically increasing `uint64` epoch counter, starting at
  epoch 1 (so 0 is reserved as an unambiguous zero-value / "never acquired" sentinel).
  A single global epoch keeps the model simple and matches the requirement's own
  suggested design ("a global (or per-fileID) monotonically increasing epoch counter").
  Per-fileID would add no benefit yet: 2a.2.2/2a.2.3's stated need is "identify old
  versions no longer referenced by ANY live snapshot" globally, and a global counter is
  sufficient and simpler for that.
