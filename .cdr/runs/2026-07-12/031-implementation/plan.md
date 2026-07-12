# Plan — Subtask 7.3.1

1. Create `engine/loadtest/soak_test.go` with:
   - Package-level doc comment on `TestSoak` explaining the honest duration-scaling
     judgment call (default minutes, `SOAK_DURATION` env var for a real multi-hour
     run later), citing subtask 6.3.4's precedent.
   - Real fixture: `catalog.Open` + `catalog.NewCatalog`, `wal.OpenWriter`,
     `mvcc.NewVersionWriter`, `mvcc.NewEpochManager` — identical building blocks to
     `query_latency_test.go`, under a fresh `t.TempDir()`.
   - Pre-seed a pool of fileIDs with an initial committed version (same shape as
     `query_latency_test.go`).
   - Launch a fixed pool of writer goroutines that loop `CommitVersion` calls against
     random pool members until a shared deadline (`time.Now().Add(duration)`),
     recording per-call errors via atomic counters; likewise a fixed pool of query
     goroutines looping `SnapshotRead` until the same deadline. Use
     `sync.WaitGroup` to join both pools cleanly (no `harness.Run` since it is
     iteration-count-based, not deadline-based — see requirement.md non-goals).
   - Capture `runtime.NumGoroutine()` and `runtime.ReadMemStats` before starting the
     goroutine pools (after `runtime.GC()`) and again after `wg.Wait()` returns (after
     another `runtime.GC()` + brief settle sleep).
   - Assert: zero ingestion/query errors; goroutine count at end does not exceed
     start by more than a generous fixed slack (accounts for GC/finalizer/test
     runtime goroutines, not the workload's own goroutines since all workload
     goroutines have already been joined by `wg.Wait()` at that point — so any
     genuine leak from the workload itself would show up as extra, non-transient
     goroutines); heap-alloc growth logged and checked against a generous ceiling
     multiple, both values always logged via `t.Logf` regardless of pass/fail.
   - `testing.Short()` skip, matching `split_race_scale_test.go`'s convention, since
     even the scaled-down default is a real multi-minute run, not appropriate for
     `-short` mode.
2. Determine the default `SOAK_DURATION` empirically: run a short calibration first
   (e.g. 15s) to confirm the harness works and get a real throughput number, then
   pick a final default (~2-3 minutes) that is session-practical to actually execute
   as part of this implementation step, consistent with 6.3.4's "empirically
   calibrated, not guessed" approach.
3. Run `go build ./engine/...` and `go vet ./engine/loadtest/...` for a fast
   sanity pass before the real timed run.
4. Run the REAL soak test: `go test ./engine/loadtest/... -run TestSoak -race -v
   -timeout <headroom over SOAK_DURATION>`. Capture full output.
5. If the real run surfaces any panic/crash/leak/correctness failure: diagnose root
   cause by reading the implicated engine/{catalog,wal,mvcc} source, apply the
   minimal fix, re-run to confirm green, and document the fix distinctly in the
   commit message (scoped, separate paragraph) per the task's explicit instruction.
6. Run the full existing `engine/loadtest` package test suite (`go test
   ./engine/loadtest/... -race`, excluding -run filter) to confirm no regression to
   6.3.1-6.3.4's existing tests from anything touched.
7. Write self-consistency.json with real captured build/vet/test output and timing.
8. Single local commit (type: summary / Problem / Solution / Impact style) for
   `engine/loadtest/soak_test.go` (+ any real fix file, called out explicitly).
9. Write handoff.json (pointers only) and stop. Do not push, do not self-verify.
