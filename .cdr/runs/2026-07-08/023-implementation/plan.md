# Plan — task-3.2.1

1. Write `proto/hivemind.proto`:
   - `syntax = "proto3"`, `package hivemind.v1`.
   - Go/Python codegen options (`option go_package`).
   - `EdgeType` enum matching `engine/graph/edge.go`'s canonical names
     (EDGE_TYPE_UNSPECIFIED=0, ENTITY_COOCCUR, LLM_ASSERTED, SPLIT_SIBLING,
     REDIRECT).
   - Messages + one `HiveMind` service with the six RPCs: PutSegment,
     GetFile, ReadPartial, GraphNeighbors, SearchCandidates, ProposeSplit.
2. Add `google.golang.org/grpc` and `google.golang.org/protobuf` to
   `engine/go.mod` (via `go get`), `go mod tidy`.
3. Install `protoc-gen-go`/`protoc-gen-go-grpc` (already done during
   discovery) and generate Go stubs into `engine/rpc/gen/`.
4. Generate Python stubs via `agents/.venv`'s `grpc_tools.protoc` into
   `agents/` (module-friendly location, e.g. `agents/hivemind_pb2*.py`).
5. Update `proto/README.md` to point at the real file and note the exact
   codegen commands used (so 3.2.2/3.2.3 authors don't have to rediscover
   them).
6. Update `docs/LLD/rpc.md`'s status line (scaffold-only -> contracts
   defined, server/client still pending) — doc-only, no semantic content
   change to the RPC list itself.
7. `gofmt`/`go vet`/`go build ./...` on `engine/`; confirm Python stubs
   import cleanly (`python -c "import hivemind_pb2, hivemind_pb2_grpc"`).
8. Self-consistency check, commit, handoff.

Explicitly out of scope for this commit (deferred to 3.2.2-3.2.5):
`engine/rpc/server.go` handler implementations, `engine/split/proposer_grpc.go`,
interceptors, integration test.
