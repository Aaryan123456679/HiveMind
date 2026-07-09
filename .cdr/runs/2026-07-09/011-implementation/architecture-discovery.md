# Architecture discovery: task-3.2.5

## Index-first findings (`.cdr/index/task.jsonl`, `docs/LLD/rpc.md`)

- task-3.2.1 (verified): `proto/hivemind.proto`, 6 RPCs defined, Go stubs in `engine/rpc/gen/`.
- task-3.2.2 (verified, PASS_WITH_COMMENTS): `engine/rpc/server.go`'s `Server` implements 5 of
  6 RPCs by embedding `hivemindv1.UnimplementedHiveMindServer` for `ProposeSplit`.
  `engine/rpc/server_test.go`'s `TestRPCServerHandlers` calls handler methods DIRECTLY
  (`f.srv.PutSegment(ctx, req)`) -- no gRPC transport at all, not even bufconn.
- task-3.2.3 (verified): `engine/split/proposer_grpc.go`'s `GRPCSplitProposer` -- real
  `hivemindv1.HiveMindClient`-backed `SplitProposer`. `engine/split/proposer_grpc_test.go`
  uses bufconn + a hand-rolled fixture gRPC server (not the real `rpc.Server`).
- task-3.2.4 (verified): `engine/rpc/interceptor.go`'s `LatencyInterceptor`.
  `engine/rpc/interceptor_test.go` wires it via `grpc.NewServer(grpc.UnaryInterceptor(...))`
  bound to bufconn -- this is a TEST-only wiring site, not production code. No production
  call site anywhere in the repo constructs a `grpc.Server` with this interceptor attached
  (confirmed by `grep -rn "grpc.NewServer" engine/ api/` below).

## docs/LLD/rpc.md (read in full)

States 3.2.5 explicitly as the last open item: "the cross-process integration test
(task-3.2.5) [is] not yet implemented." Confirms `ProposeSplit`'s server side is
permanently out of scope for issue #16 (deferred to issue #18), and that `engine/rpc/`
serves exactly 5 RPCs in production.

## grep evidence: no existing real grpc.Server / net.Listener production wiring

```
$ grep -rn "grpc.NewServer" engine api --include=*.go
engine/rpc/interceptor_test.go:53:  gsrv := grpc.NewServer(grpc.UnaryInterceptor(...))   (test-only, bufconn)
engine/split/proposer_grpc_test.go:68: srv := grpc.NewServer()                             (test-only, bufconn)
$ grep -rn "net.Listen(" engine api --include=*.go
(no matches)
```
`api/main.go` is an HTTP gateway (no gRPC server construction at all). Confirms this
integration test is the first call site anywhere in the repo -- test or production -- that
wires `LatencyInterceptor` into a `grpc.Server` bound to a real OS-level `net.Listener`.

## Fixture composition pattern to mirror

`engine/rpc/server_test.go`'s `newFixture` and `engine/integration_test.go`'s
`TestStorageCoreIntegration` both build: `catalog.Open` -> `catalog.NewCatalog` ->
`catalog.NewIDAllocator` -> `wal.OpenWriter` -> `catalog.OpenContentStore` ->
`btree.OpenIndexFile` -> `btree.NewNodeStore` -> `btree.NewNodeAllocator` -> seed files via
`cs.Create` + WAL-logged `btree.Insert` -> `graph.BuildCSR(adjacency)` for the graph.
`rpc.NewServer(cat, cs, idAlloc, g, btreeStore, btreeRootNodeID)` assembles the real `Server`.
This run's test reuses the exact same composition, then additionally: starts a real
`net.Listener` + `grpc.Server` (with `LatencyInterceptor` wired via `grpc.UnaryInterceptor`),
registers the `Server` via `hivemindv1.RegisterHiveMindServer`, dials a real
`grpc.NewClient`/`grpc.Dial` connection, and drives all 5 RPCs plus `ProposeSplit` through
that real client -- rather than calling handler methods directly (3.2.2's style) or using
bufconn (3.2.4's/3.2.3's style).

## Judgment call: real net.Listener over bufconn

Issue text does not mandate either transport for 3.2.5. Since 3.2.2/3.2.3/3.2.4's existing
tests already cover bufconn-based and direct-call testing styles, using `net.Listen("tcp",
"127.0.0.1:0")` here makes this test genuinely distinguishable as "integration" (full
OS-level socket + TCP handshake + real network framing) rather than duplicating the existing
in-process bufconn pattern. Disclosed as a judgment call, not an issue requirement.

## GRPCSplitProposer error-wrapping (engine/split/proposer_grpc.go)

`ProposeSplit` wraps every non-nil RPC error as:
`fmt.Errorf("split: GRPCSplitProposer.ProposeSplit: RPC failed (%s): %w", status.Code(err), err)`
Against the real (unmodified) `Server`, `ProposeSplit` falls through to
`UnimplementedHiveMindServer`'s default, which returns a `status.Error(codes.Unimplemented, ...)`.
This test asserts `errors.Is`-independent style: `status.Code(err) == codes.Unimplemented` via
`status.FromError(err)` on the wrapped error (verifying `%w` preserves gRPC-status
introspectability through `errors.As`/`status.FromError`).
