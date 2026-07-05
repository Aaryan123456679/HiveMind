# Subtask 2a.2.2 (issue #7) — Background compactor reclaiming eligible old versions

Acceptance criteria: a version file is deleted only once its epoch's refcount is zero
and it is not the current version; the current version is never reclaimed regardless
of refcount.

Test spec: `go test ./engine/mvcc/... -run TestCompactor -race`: create several old
versions, hold some snapshots open, run compactor, assert only eligible versions are
removed.

Impacted modules: engine/mvcc/gc.go, engine/mvcc/gc_test.go.

Context: 2a.2.1 (EpochManager: NewEpochManager, CurrentEpoch, AdvanceEpoch,
AcquireCurrentEpoch, Release, RefCount, MinReferencedEpoch) is done/verified but NOT
wired into Snapshot (read.go) or CommitVersion (write.go) — deferred explicitly to this
subtask. This subtask must (a) wire EpochManager into Snapshot/CommitVersion, (b)
implement the compactor itself.
