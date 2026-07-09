# Plan — task-3.2.4

1. `engine/rpc/interceptor.go`:
   - `RPCMetric` struct: Method, Duration, RequestBytes, ResponseBytes, Code, Err.
   - `Recorder` interface with `Record(RPCMetric)`.
   - `SlogRecorder` default implementation (wraps `*slog.Logger`, defaults to
     `slog.Default()`), emits a structured line: method, duration_ms, request_bytes,
     response_bytes, code — a format a future benchmark harness can parse (structured
     key=value / JSON-capable via slog handler choice, not a bespoke text format).
   - `Option`/`WithRecorder` functional option.
   - `LatencyInterceptor(opts ...Option) grpc.UnaryServerInterceptor`: wraps `handler`,
     records wall-clock start via `time.Now()`, calls `handler`, computes `time.Since`,
     computes byte sizes via `proto.Size` on request/response messages (best-effort —
     falls back to 0 if not a `proto.Message`), maps returned error to a `codes.Code` via
     `status.Code(err)`, calls `Recorder.Record`. Returns resp/err unchanged (no behavior
     change).
2. `engine/rpc/interceptor_test.go`:
   - `TestLatencyInterceptor`: bufconn + real `grpc.NewServer(grpc.UnaryInterceptor(...))`
     wrapping a real `rpc.Server` (reusing `newFixture`'s stack from server_test.go) +
     real dialed client. Custom in-test `Recorder` capturing emitted `RPCMetric`s via a
     channel/slice guarded by a mutex (concurrent-safe, since interceptor runs per-RPC and
     the test issues concurrent calls too). Subtests:
     - success call (GetFile) -> exactly one record, Code=OK, Duration>0, sizes>0.
     - error call (GetFile NotFound) -> exactly one record, Code=NotFound, Duration>0,
       error path did not change response semantics (still a real gRPC error from the
       client's perspective).
     - concurrent calls (N goroutines) -> N records total, race-clean.
3. `engine/rpc/server.go`: add `FileId==catalog.InvalidFileID` guard at top of `GetFile`
   and `ReadPartial`, returning `codes.InvalidArgument`.
4. `engine/rpc/server_test.go`: add `GetFile_ZeroFileID` / `ReadPartial_ZeroFileID`
   subtests asserting `codes.InvalidArgument`.
5. Run gofmt/vet/build/test (-race where concurrency-relevant, explicit -timeout).
6. Update `.cdr/index/regression.jsonl` + `.cdr/memory/pending.md` marking the FileId=0
   entry resolved.
7. Two commits: (a) interceptor.go + interceptor_test.go (3.2.4 proper), (b) FileId=0
   fix + regression tests + index/memory updates (explicitly-flagged bundled follow-up).
