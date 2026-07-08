# Fix cycle notes — task-3.2.1 (issue #16), attempt 1 of 3

Source: verification verdict `.cdr/runs/2026-07-08/024-verification/verification.json` (CHANGES_REQUESTED).

## F1 (blocking) — fixed

`proto/hivemind.proto:103` declared `Neighbor.weight` as `float weight = 3;`. The Go field it
claims to mirror field-for-field, `engine/graph.CSREdge.Weight` (`engine/graph/csr.go:82`), is
`uint32` — an integer running sum aggregated in `compact.go`'s `mergeEdges`
(`sum.Weight = prev.Weight + e.Weight`) and compared with exact `!=`/`>` in `traverse.go`'s sort
tie-break. Confirmed both source locations before changing.

Change: `float weight = 3;` -> `uint32 weight = 3;` in `proto/hivemind.proto`.

Regenerated stubs (did NOT hand-edit generated files):
- Go: installed `protoc-gen-go`/`protoc-gen-go-grpc` via `go install ...@latest` (per
  `proto/README.md`'s documented invocation), ran
  `protoc --go_out=engine --go_opt=module=github.com/Aaryan123456679/HiveMind/engine
  --go-grpc_out=engine --go-grpc_opt=module=github.com/Aaryan123456679/HiveMind/engine
  proto/hivemind.proto`.
  Result: `engine/rpc/gen/hivemind.pb.go`'s `Neighbor.Weight` field changed
  `float32` (wire type `fixed32`) -> `uint32` (wire type `varint`); `GetWeight()` return type
  updated accordingly. `hivemind_grpc.pb.go` had no diff (no field-level content).
- Python: used the venv at `agents/.venv` (has `grpcio-tools` 1.81.1 already installed), ran
  `python -m grpc_tools.protoc -I proto --python_out=agents --grpc_python_out=agents
  --pyi_out=agents proto/hivemind.proto` from repo root, per `proto/README.md`.
  Result: `agents/hivemind_pb2.py`'s serialized descriptor updated (`weight` field tag byte
  `\x02` (TYPE_FLOAT) -> `\x0d` (TYPE_UINT32)); `agents/hivemind_pb2.pyi`'s `Neighbor.weight`
  type annotation `float` -> `int`. `agents/hivemind_pb2_grpc.py` had no diff (no field-level
  content).

Both toolchains (`protoc-gen-go`/`protoc-gen-go-grpc` v1.36.x/v1.6.x via `go install`, and
`grpc_tools.protoc` via the existing `agents/.venv`) were available in-sandbox this cycle, so this
is a genuine byte-for-byte regeneration, not a hand-edit or structural-only check (unlike
verification run 024's degraded-confidence note about missing plugins).

Verified: `gofmt -l .` clean, `go vet ./...` clean, `go build ./...` clean (all from `engine/`).

## F2 (non-blocking, disclosed) — addressed cheaply

Added a short clarifying note to `docs/LLD/rpc.md`'s `Split` bullet under "Exposed RPCs" (a
sub-bullet on the existing `Split` line) stating explicitly that `Split` is intentionally **not**
part of `proto/hivemind.proto`'s gRPC surface — issue #16's task-3.2.1 acceptance criteria name
exactly 6 RPCs (dropping `Split`, including `ProposeSplit`), `Split` is invoked in-process within
`engine/` rather than over the wire, and any future cross-process exposure of `Split` should be a
separately-scoped later subtask rather than folded silently into 3.2.2's server surface.

Did NOT implement `Split` as a 7th RPC (out of scope for this fix cycle per instructions) and did
NOT touch `proto/hivemind.proto`'s RPC list (still the literal 6 from the issue).

## Verification run

- `go build ./...` (from `engine/`): exit 0.
- `gofmt -l .`: no output (clean).
- `go vet ./...`: no output (clean).
- `go test ./... -count=1 -timeout 25m` (from `engine/`): all packages PASS except
  `TestReaderDuringSplit` (`engine/split`), which is the exact pre-existing baseline flake named
  in verification run 024's `test_suite` check ("known pre-existing baseline flake ...
  race-timing test, present since before this subtask -- matches expected baseline exactly").
  No new failures.
