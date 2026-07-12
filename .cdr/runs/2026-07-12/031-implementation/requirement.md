# Requirement — Subtask 7.3.1 (GitHub issue #36, Phase 7: Buffer/polish)

## Source

Issue #36, "[7] Soak-test fixes", subtask 7.3.1: "Extended soak test (long-duration
concurrent ingestion + query) with fixes for surfaced issues."

## Acceptance criteria (verbatim from task)

A soak test run over an extended duration (e.g. hours) under sustained concurrent
ingestion+query load surfaces no unrecovered crashes, leaks, or correctness
violations; any issues found are fixed.

## Test spec (verbatim from task)

`go test ./engine/loadtest/... -run TestSoak -timeout <extended>` (or a standalone
soak script): run for the extended duration, assert no crashes/panics and stable
memory/goroutine counts at the end.

## Impacted modules

`engine/loadtest/soak_test.go` (new file).

## Engineering judgment call (duration scaling — same pattern as 6.3.4)

A literal multi-hour soak test cannot actually be executed and verified within this
implementation session. Following the precedent set by subtask 6.3.4
(`TestAutoSplitRaceAtScale`, engine/loadtest/split_race_scale_test.go), which
empirically measured real per-op cost and then deliberately scaled a "load-test
scale" scenario to a bounded, CI-viable, still-genuinely-stressful duration rather
than either faking a multi-hour run or skipping the point of the test entirely,
this subtask's `TestSoak` will:

- Run sustained concurrent ingestion + query load against the real storage engine
  (catalog + WAL + MVCC, mirroring `query_latency_test.go`'s fixture, LLM/segmentation
  boundary mocked exactly per 6.3.2's precedent) for a bounded but real wall-clock
  duration, default a few minutes, overridable via a `SOAK_DURATION` environment
  variable (Go duration string, e.g. `SOAK_DURATION=2h`) so a genuine multi-hour run
  CAN be performed later (e.g. in CI or a dedicated soak environment) without any
  code change.
- Capture `runtime.NumGoroutine()` and `runtime.ReadMemStats` heap-alloc figures at
  the start and end of the soak window (after a `runtime.GC()` at each end, so memory
  comparison is not polluted by not-yet-collected garbage) and assert no unbounded
  growth (goroutine leak check: end count must not exceed start count by more than a
  small, generously-sized worker-count-relative slack; heap growth check: logged and
  bounded by a generous ceiling, since some steady-state growth from the storage
  engine's own on-disk/in-memory bookkeeping across a real ingestion+query workload
  is expected and legitimate, not itself a leak).
- Assert zero errors/panics from every ingestion and query call across the run.
- Document, in a code comment on `TestSoak`, exactly why the default duration is
  minutes and not hours, and how to run it for real hours via `SOAK_DURATION`. This
  must be an honest disclosure, not a claim that a few minutes literally simulates
  hours.
- Actually be run (via `go test -race`) as part of this implementation, with real
  captured output (elapsed time, goroutine/memory deltas, throughput). Any real
  crash/leak/correctness issue surfaced must be fixed as part of this subtask
  (acceptance criteria explicitly requires "any issues found are fixed").

## Non-goals

- Not modifying `engine/loadtest/harness.go`'s `Run()` API (iteration-count-based);
  the soak test drives its own duration-bounded loops directly with
  `sync.WaitGroup` + atomics for counters, since `harness.Run` is not duration-aware
  (fixed `Workers x Iterations`), matching this file's own stated need for an
  open-ended "run until deadline" shape rather than a fixed op count. This mirrors
  how `split_race_scale_test.go` already reimplements fixture setup where an existing
  API doesn't fit, without changing existing package APIs.
- Not implementing multi-hour CI infrastructure; only wiring the env-var-driven
  duration knob into this one test file.
