# Architecture discovery — task-3.2.4

## Index / prior LLD context read (before source)

- `docs/LLD/rpc.md`: confirms interceptor scope — latency both sides, LLM cost
  Python-side only, feeds a future benchmark harness (Epic 5 / eval.md). Status line at
  top of the LLD explicitly lists "Per-call latency/cost interceptor (task-3.2.4) ...
  not yet implemented" as of last sync — confirms no prior Go-side interceptor exists.
- `.cdr/index/regression.jsonl` (issue 16 / subtask 3.2.2 entry, run
  `.cdr/runs/2026-07-09/003-verification`): FileId=0 -> codes.Internal finding, confirmed
  unresolved, recommends folding into 3.2.4.
- `.cdr/memory/pending.md`: same finding, phrased as a Phase 3 follow-up item, explicitly
  says "no dedicated GitHub issue created directly for it now" and defers to 3.2.4/standalone.
- 3.2.3's `engine/split/proposer_grpc_test.go` establishes this repo's real-wire gRPC test
  convention for `engine/rpc/gen` services: `bufconn.Listen` + real `grpc.NewServer` +
  real `grpc.NewClient` (not `grpc.Dial`, matches the grpc-go version in go.mod) + a
  fixture server embedding `hivemindv1.UnimplementedHiveMindServer`. This is the pattern
  task-3.2.4's own test spec (`TestLatencyInterceptor`) should mirror, since the parent
  dispatch explicitly asks for genuine-wire-transport tests, not direct interceptor-function
  calls.

## Direct source reads (after index/LLD exhausted)

- `engine/rpc/server.go` (323 lines, read in full): `Server` struct + 5 handlers
  (`PutSegment`, `GetFile`, `ReadPartial`, `GraphNeighbors`, `SearchCandidates`) +
  `mapCatalogError` + edge-type conversion helpers. No `grpc.NewServer(...)` construction
  site exists anywhere in `engine/rpc/` — `NewServer` here only builds the `*Server`
  RPC-handler adapter, not a `*grpc.Server`. Confirmed via repo-wide grep: the only
  `grpc.NewServer()` call in the whole repo is `engine/split/proposer_grpc_test.go:68`
  (a test-only fixture). There is no `cmd/`-style main that wires up a real
  `*grpc.Server` serving `engine/rpc`'s `Server` over a real listener yet — that
  presumably belongs to a later subtask (3.2.5 integration test, or a future
  server-binary entry point not yet built). This changes the "wire into server.go's gRPC
  server construction" instruction: since no such construction call exists in this repo
  today, "wiring in" the interceptor here means exposing a
  `grpc.UnaryServerInterceptor`-returning constructor from `engine/rpc/interceptor.go`
  that any future `grpc.NewServer(grpc.UnaryInterceptor(...))` call site (a server binary,
  or 3.2.5's integration test) is expected to pass in — plus demonstrating that wiring
  concretely inside the interceptor's own test file (which stands up a real
  `grpc.NewServer` for `TestLatencyInterceptor`, the only place in `engine/rpc/` a real
  `*grpc.Server` is constructed against the production `Server`).
- `engine/catalog/catalog.go` (`Catalog.Get`, ~line 212-221): confirms `FileID ==
  InvalidFileID` returns a plain (non-`ErrNotFound`-wrapped) error, matching the
  regression finding exactly.
- `engine/rpc/server_test.go`: confirms existing test fixture (`newFixture`) builds a
  real `catalog`/`content`/`btree`/`graph` stack, but currently calls `Server` methods
  directly (`s.srv.GetFile(ctx, req)`), not over a real gRPC connection — this is fine
  for handler-logic tests but is exactly why the interceptor's own tests must use
  bufconn/real dial, per the parent dispatch's explicit instruction not to test interceptor
  wiring via direct function calls only.

## Decision

- Add `engine/rpc/interceptor.go`: exports
  `LatencyInterceptor() grpc.UnaryServerInterceptor` (constructor, in case future config
  is needed) that measures wall-clock latency via `time.Since`, and a
  `RequestMetrics`/similar hook exposing a per-call record (method name, duration,
  request/response proto byte sizes as the disclosed proxy "cost", success/failure code)
  in a form a benchmark harness can consume. Kept dependency-free (no external
  metrics/logging library pulled in) — record emission goes through a small injectable
  `Recorder` interface (default: structured `log/slog` line), so callers (or future
  benchmark-harness code) can plug in their own sink without changing the interceptor.
- Add `engine/rpc/interceptor_test.go`: `TestLatencyInterceptor` spins up a real
  `grpc.NewServer(grpc.UnaryInterceptor(LatencyInterceptor(...)))` over bufconn, wraps
  the production `rpc.Server` (via `newFixture`'s existing real catalog/content stack)
  registered as the `HiveMindServer`, issues real RPCs over a real dialed client
  connection, and asserts a latency record was emitted per call (including for an
  error-returning call, e.g. `GetFile` NotFound) — matching the issue's exact test-spec
  name and the "assert a latency record emitted per call" wording.
- Fix `FileId==0` misclassification in `server.go`'s `GetFile`/`ReadPartial`: add an
  explicit `req.GetFileId() == catalog.InvalidFileID` guard returning
  `codes.InvalidArgument` before calling into `s.cs`/`s.cat`, plus regression subtests.
  Committed separately from the interceptor work.
