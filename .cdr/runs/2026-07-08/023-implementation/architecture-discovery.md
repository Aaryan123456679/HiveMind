# Architecture discovery — task-3.2.1

## Index / prior-run trail consulted first

- `.cdr/index/task.jsonl`: no `task-3.2.x` entries exist yet. Nearest prior
  work: `task-3.1.4`/`task-3.1.5` (issue #15, `engine/graph/`'s
  `GraphNeighbors`/edge-type validation) and `task-2b.2` (issue #11,
  `engine/split/`'s `SplitProposer` interface + `MockSplitProposer`).
- `.cdr/runs/2026-07-08/`: latest prior run is `022-cdr-commit`; this run is
  `023-implementation`.

## HLD (`docs/HLD.md`)

Confirms the gRPC boundary sits between the Go storage engine (`engine/rpc`)
and the Python ML/agent service (`agents/`), gated behind `proto/`.

## LLD (`docs/LLD/rpc.md`)

Explicitly "Status: scaffold only" and documents the exact six RPCs this
issue names (five engine-server RPCs — PutSegment, GetFile, ReadPartial,
`Split` [engine-internal, distinct from the six client-facing RPCs the
issue's Test spec enumerates — issue text says "GraphNeighbors,
SearchCandidates" server-side and "ProposeSplit" client-side, matching six
total when Split is counted separately as engine-internal-only, not
proto-exposed at this stage; kept `Split` out of the .proto to match the
issue's literal six-RPC list], GraphNeighbors, SearchCandidates — plus one
consumed RPC, `ProposeSplit`, called *from* `engine/split/` *to* the Python
agent service. Also states contracts are "not yet written at this scaffold
stage" — confirming no prior `.proto` content exists to preserve/migrate.

## `proto/README.md`

"Generated Go/Python stubs are not checked in — regenerate via `protoc`
(tooling to be added alongside the first real `.proto` file)." This run is
that first real `.proto` file; per dispatch instructions I did NOT introduce
new build tooling beyond what's needed — used the already-present system
`protoc` binary (libprotoc 3.20.3) plus `go install` for
`protoc-gen-go`/`protoc-gen-go-grpc` (consistent with how this repo already
manages Go tooling: no vendored binaries, plain `go install`/`go.mod`
managed deps). Python side already has `grpcio-tools` declared in
`agents/pyproject.toml` (from issue #11 setup) and installed in
`agents/.venv`.

## Existing scaffolds read directly

- `engine/rpc/doc.go` — single-line package placeholder, nothing to
  preserve/break.
- `engine/split/proposer.go`, `engine/split/proposer_mock.go` — confirmed no
  gRPC/protobuf import anywhere (matches task-2b.2's verified note: "Zero
  gRPC/protobuf imports anywhere in split/'s dependency graph"). This run's
  `.proto` file does not touch `engine/split/` at all — 3.2.3 wires it up
  later.
- `engine/graph/traverse.go`'s `GraphNeighbors(g *CSRGraph, fileID uint64,
  depth int, edgeTypeFilter EdgeType, maxNodes int) ([]CSREdge, error)` and
  `engine/graph/edge.go`'s `EdgeTypeName`/`ParseEdgeType` (canonical wire
  names ENTITY_COOCCUR/LLM_ASSERTED/SPLIT_SIBLING/REDIRECT) — used to shape
  the `GraphNeighborsRequest`/`GraphNeighborsResponse` messages and the
  proto `EdgeType` enum so 3.2.2's server implementation can map 1:1
  without inventing a second edge-type vocabulary.
- `engine/catalog/content.go`'s `ReadPartial(fileID uint64) ([]HeaderOffset,
  error)` and `HeaderOffset{Header string; Offset int}` — used to shape
  `ReadPartialResponse`.
- `docs/LLD/ingestion-agent.md`'s literal shape note:
  `ProposeSplit(fileContent) -> [{newPath, sectionRanges}, ...] + redirect
  summary` — matches `engine/split/proposer.go`'s `SplitPlan`/
  `SplitFileProposal`/`SectionRange` field-for-field. Used directly to shape
  `ProposeSplitRequest`/`ProposeSplitResponse` so 3.2.3's real
  `proposer_grpc.go` can convert without semantic drift.

## Gap noted

No dedicated proto/gRPC LLD subsection beyond `docs/LLD/rpc.md`'s RPC list
existed to lift message field names from verbatim — message shapes in this
run are designed directly from (a) the LLD's RPC list, (b) the concrete Go
function signatures of the modules each RPC delegates to, and (c) the split
LLD's literal `ProposeSplit` shape note. This is a disclosed judgment call,
not a silent invention.
