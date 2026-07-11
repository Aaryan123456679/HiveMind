# Plan — Subtask 4.5.3.3

1. Add `sync`/`time` imports to `orchestrate.go`.
2. Extend `Orchestrator` struct with `now func() time.Time`, `leaseDuration
   time.Duration`, `mu sync.Mutex`, `leases map[uint64]time.Time`.
3. Add `DefaultSplitLeaseDuration` const (30s) and a functional-options
   pair (`Option`, `WithClock`, `WithLeaseDuration`), matching the
   `engine/rpc` idiom already used in this repo for injectable clocks.
4. Widen `NewOrchestrator` with variadic `opts ...Option`, defaulting `now`
   to `time.Now` and `leaseDuration` to `DefaultSplitLeaseDuration`
   (backward compatible with existing 3-arg call sites).
5. Split `BeginSplit`'s post-`TryAcquire` logic into a new
   `finishBeginSplit` helper (transition + guard-release-on-failure, same
   as before) plus a new `recordLease` call on success.
6. Change `BeginSplit`'s guard-busy path: instead of immediately returning
   `ErrAlreadySplitting`, call `reclaimIfExpired(fileID)`; if it reclaimed,
   retry `TryAcquire` once and proceed via `finishBeginSplit`; otherwise
   fall through to the existing `ErrAlreadySplitting` return.
7. Implement `reclaimIfExpired`: look up and (if expired) delete the lease
   entry under `o.mu`; if expired, force-transition
   `StatusSplitting -> StatusActive` via the existing `transitionStatus`
   primitive and `guard.Release(fileID)`; report whether it acted.
8. Implement `recordLease`/`clearLease`; wire `clearLease` into `EndSplit`
   via an additional `defer`.
9. Update doc comments: `Orchestrator`'s package-level doc bullet that
   previously disclosed "no automatic recovery" as deliberately deferred;
   `BeginSplit`'s and `EndSplit`'s doc comments to describe the new
   lease/reclaim behavior.
10. Add `TestAbandonedSplittingRecoversAfterTimeout` to
    `orchestrate_test.go` per the exact test spec, using an injected fake
    clock (`atomic.Int64` nanos + closure) — no real sleep.
11. Build, vet, and run the new test plus the rest of the package's tests
    (excluding the concurrently-in-progress `split_race_test.go`'s tests,
    per explicit scope-isolation instructions) under `-race`.
12. Write CDR artifacts, stage only the two in-scope files plus this run's
    own `.cdr/runs/...` directory, and create one local commit.
