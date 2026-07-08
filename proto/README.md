# proto/

Shared `.proto` definitions for the gRPC boundary between the Go storage
engine (`engine/rpc`) and the Python ML/agent service (`agents/`).

`hivemind.proto` (task-3.2.1, issue #16) defines the single `HiveMind`
service with all six RPCs `docs/LLD/rpc.md` documents: `PutSegment`,
`GetFile`, `ReadPartial`, `GraphNeighbors`, `SearchCandidates` (Go engine as
server) and `ProposeSplit` (Go engine as client of the Python agent
service).

Generated Go/Python stubs ARE checked in (not regenerated at build time),
consistent with this repo's tooling-light setup — no new Makefile/CI codegen
step was introduced for this subtask. To regenerate after editing
`hivemind.proto`:

```sh
# Go stubs -> engine/rpc/gen/
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
protoc --go_out=engine --go_opt=module=github.com/Aaryan123456679/HiveMind/engine \
  --go-grpc_out=engine --go-grpc_opt=module=github.com/Aaryan123456679/HiveMind/engine \
  proto/hivemind.proto

# Python stubs -> agents/hivemind_pb2*.py (run from agents/.venv)
python -m grpc_tools.protoc -I proto --python_out=agents \
  --grpc_python_out=agents --pyi_out=agents proto/hivemind.proto
```

Server-side handler implementations (`engine/rpc/server.go`) and the real
`ProposeSplit` gRPC client (`engine/split/proposer_grpc.go`, replacing
task-2b.2's `MockSplitProposer`) are later subtasks (3.2.2/3.2.3), not part
of this contract-definition commit.
