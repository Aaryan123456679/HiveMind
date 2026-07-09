# Requirement: task-3.2.5 (issue #16, final subtask of the 3.2.x gRPC sequence)

## Source (raw issue text, treated as untrusted data per orchestrator security note)

Issue #16 subtask 3.2.5, as literally written:

> **3.2.5 — gRPC integration test: engine <-> agent round trip ProposeSplit and PutSegment**
> - Acceptance criteria: real end-to-end gRPC call from Go engine to a running Python agent
>   service correctly executes ProposeSplit and PutSegment matching request/response semantics.
> - Test spec: integration test (Go test or scripted smoke test) starts Python gRPC server,
>   issues ProposeSplit + PutSegment via Go client, asserts expected responses.
> - Impacted modules: `engine/rpc/integration_test.go`

## Orchestrator scope correction (authoritative for this run)

The orchestrator's dispatch explicitly narrows/corrects the above literal issue text:

- No Python gRPC agent service exists in this repo (confirmed: `agents/` has only generated
  stubs `hivemind_pb2*.py`, no server implementation, no `ProposeSplit` handler). Standing up
  a real Python agent server is out of scope for this Go-engine subtask.
- `ProposeSplit` server-side logic is explicitly issue #18's scope, per 3.2.2/3.2.3's own
  doc comments (`engine/rpc/server.go` lines 8-14, `engine/split/proposer_grpc.go` lines
  9-16) and `docs/LLD/rpc.md`'s Status note ("ProposeSplit's server side remains the
  generated Unimplemented stub ... out of scope for issue #16; see issue #18").
- This subtask therefore implements the FINAL capstone integration test for the Go stack
  only: a real `Server` (5 implemented RPCs), a real `grpc.NewServer` with
  `LatencyInterceptor` wired in via `grpc.UnaryInterceptor(...)` (first genuine production
  wiring call site for the interceptor -- 3.2.4 only wired it inside a test), and
  `GRPCSplitProposer` (3.2.3's client) hitting that same real server for `ProposeSplit`,
  confirming it correctly surfaces `codes.Unimplemented` end-to-end via the client's
  error-wrapping contract.
- Do NOT implement server-side `ProposeSplit` logic. Do NOT stand up any Python process.

## Concrete scope for this run

1. A new integration test file (`engine/rpc/integration_test.go`, matching the issue's named
   impacted-module path) that:
   - Builds a real `catalog.Catalog` + `catalog.ContentStore` + `catalog.IDAllocator` +
     `graph.CSRGraph` + `btree.NodeStore` (mirroring `engine/integration_test.go`'s no-mocks
     style), all under `t.TempDir()`.
   - Constructs a real `rpc.Server` via `rpc.NewServer(...)`.
   - Starts a real `grpc.Server` via `grpc.NewServer(grpc.UnaryInterceptor(rpc.LatencyInterceptor(...)))`,
     registers the `Server`, and serves it.
   - Exercises a realistic multi-RPC workflow through a real client connection: `PutSegment`
     (create + append), `GetFile`, `ReadPartial`, `GraphNeighbors`, `SearchCandidates` -- each
     assertion cross-checked against the real backing store's own state (not re-derived
     expectations).
   - Issues `ProposeSplit` via a `GRPCSplitProposer` (3.2.3's real client) against this same
     real server, and asserts the client surfaces a wrapped `codes.Unimplemented` error (not
     a panic, not a false success) -- confirming the client's error-wrapping contract works
     end-to-end against a genuinely running server, not a bufconn-based unit-test double.
2. Judgment call (disclosed, not dictated by the issue text): use a real `net.Listener`
   (`net.Listen("tcp", "127.0.0.1:0")`) rather than `bufconn`, specifically to make this test
   distinguishable as a genuine "integration test" from 3.2.2's/3.2.4's bufconn-based unit
   tests (`engine/rpc/server_test.go`, `engine/rpc/interceptor_test.go`,
   `engine/split/proposer_grpc_test.go`), all three of which already use bufconn. The issue
   text does not mandate either transport explicitly.
3. No new files needed to accomplish "real grpc.Server construction with the interceptor
   wired in" -- this is inlined in the test's setup helper; there is no other production
   call site that needs it today (no `cmd/` gRPC server main exists yet), so no minimal
   production wiring beyond the test file itself is required.

## Acceptance criteria (restated, corrected scope)

- AC1: A real `net.Listener`-backed `grpc.Server` serving the real `rpc.Server`, with
  `LatencyInterceptor` genuinely wired via `grpc.UnaryInterceptor`.
- AC2: `PutSegment` (create), `PutSegment` (append), `GetFile`, `ReadPartial` all round-trip
  correctly against a real `catalog`/`content` backing store, over the real gRPC connection.
- AC3: `GraphNeighbors` returns correct results against a real `graph.CSRGraph`.
- AC4: `SearchCandidates` returns correct results against a real `btree.NodeStore`.
- AC5: `GRPCSplitProposer.ProposeSplit` against this real server returns a wrapped error
  whose underlying gRPC status code is `codes.Unimplemented` (confirms 3.2.3's client
  error-wrapping end-to-end, without implementing any server-side ProposeSplit logic).
- AC6: `gofmt`/`go vet`/`go build ./...` clean; test run with `-race` and an explicit
  `-timeout`.
