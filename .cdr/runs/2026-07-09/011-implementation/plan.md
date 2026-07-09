# Plan: task-3.2.5

1. Create `engine/rpc/integration_test.go` in package `rpc_test` (external test package, at
   the `engine/rpc/` level -- consistent with the issue's named path; unlike
   `server_test.go`/`interceptor_test.go` which are internal `package rpc`, this file only
   needs exported surface, so external package keeps it an honest black-box integration
   test).
2. `newIntegrationFixture(t)` helper: build real `catalog`/`content`/`wal`/`btree`/`graph`
   state (mirroring `engine/rpc/server_test.go`'s `newFixture`, adapted to exported
   identifiers since this is an external test package), then `rpc.NewServer(...)`.
3. `startRealServer(t, srv)` helper: `net.Listen("tcp", "127.0.0.1:0")`,
   `grpc.NewServer(grpc.UnaryInterceptor(rpc.LatencyInterceptor(rpc.WithRecorder(rec))))`,
   `hivemindv1.RegisterHiveMindServer`, `go gsrv.Serve(lis)`, dial back via
   `grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))`,
   teardown via `t.Cleanup` (`gsrv.GracefulStop()`, `conn.Close()`). Use a small in-test
   `Recorder` capturing `RPCMetric`s so the test can additionally assert the interceptor is
   genuinely firing (method name + code recorded) for at least one call -- not just that
   RPCs succeed.
4. `TestRPCIntegration` (single top-level test, subtests via `t.Run`):
   - `PutSegment_Create` + `PutSegment_Append`: create a new file via the client, then
     append; assert `FileId`/`NewVersion` and cross-check content via `GetFile`.
   - `GetFile`: read back full content, compare byte-for-byte against what was written.
   - `ReadPartial`: assert header offsets for a markdown file with multiple `#`/`##`
     headers.
   - `GraphNeighbors`: seed a real `graph.CSRGraph` edge, call over the wire, assert
     target/type/weight match the seeded edge.
   - `SearchCandidates`: seed the real B+Tree with path entries, call over the wire, assert
     returned candidates match `btree.PrefixScan`'s own direct result.
   - `ProposeSplit_Unimplemented`: build a `split.GRPCSplitProposer` over the same real
     client connection, call `ProposeSplit`, assert error is non-nil and
     `status.Code(err) == codes.Unimplemented` (via `status.FromError` on the wrapped
     error).
   - After all subtests: assert the interceptor's recorder captured at least the expected
     RPC methods with `codes.OK` for the successful ones (proves real wiring, not just that
     RPCs happen to work without it).
5. Run `gofmt -l`, `go vet ./...`, `go build ./...`, then
   `go test ./engine/rpc/... -run TestRPCIntegration -race -timeout 60s -v`.
6. Write self-consistency.json, commit, handoff.json.
