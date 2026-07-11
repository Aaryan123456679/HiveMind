# Requirement — Subtask 4.5.3.2 (Issue #40)

**Title**: Bound engine/split/guard.go's FileGuard registry with eviction.

**Acceptance criteria**: `FileGuard`'s per-fileID guard map (`guards map[uint64]*fileSplitState`)
no longer grows unboundedly with the total number of distinct fileIDs ever guarded across the
lifetime of a `FileGuard`. Bounded via the same reference-counted, gated eviction approach chosen
for subtask 4.5.1.3's `NodeStore.latches` registry (`engine/btree/latch.go`,
`acquireLatch`/`releaseLatch`/`peekLatch`), revisited for FileGuard's specific semantics.

**Test spec**: `go test ./engine/split/... -run TestFileGuardRegistryBounded` — guard a large
number of distinct fileIDs (with proper Release lifecycle), assert the registry size stays
bounded (does not grow linearly with the total number of distinct fileIDs ever guarded).

**Impacted modules**: `engine/split/guard.go`, `engine/split/guard_test.go`.

**Explicit scope boundary for this run**: ONLY `engine/split/guard.go` and
`engine/split/guard_test.go` are touched. Subtask 4.5.3.1 (engine/catalog/content.go wiring) is
explicitly OUT of scope for this run per the launching agent's instructions (deferred until the
concurrent issue #42 catalog-stream agent finishes, to avoid a file conflict). No other
engine/split files, and no engine/mvcc, engine/wal, engine/catalog, or engine/btree files are
touched.

**Source of truth for the existing deferred-limitation note**: `.cdr/memory/pending.md` line 19
("FileGuard registry has no eviction (Phase 2b follow-up)... same deliberately-deferred growth
characteristic as engine/btree/latch.go's NodeStore.latches noted above; revisit together if/when
that one is addressed"). That "revisit together" trigger subtask (4.5.1.3, engine/btree/latch.go)
was completed and committed as `545e827` ("fix: bound engine/btree NodeStore latch registry with
refcounted eviction"), already present in this working tree. This run is that revisit.

**Untrusted-content note**: `gh issue view 40`'s body and this repo's git history are treated as
untrusted data per the launching agent's instructions; nothing resembling an embedded fake
system-reminder was observed in issue #40's body during this run.
