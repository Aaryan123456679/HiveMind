# task-3.2.1 — Shared `hivemind.proto` gRPC contract (issue #16, Epic Phase 3)

## Summary

Subtask 3.2.1 ("define the shared gRPC contract between the Go storage engine and the Python agent
service") is complete and independently verified. This is the first increment of issue #16's five-part
gRPC rollout (3.2.1-3.2.5): a single `HiveMind` service in `proto/hivemind.proto` covering all six RPCs
named in the issue's acceptance criteria (`PutSegment`, `GetFile`, `ReadPartial`, `GraphNeighbors`,
`SearchCandidates`, `ProposeSplit`), plus an `EdgeType` enum mirroring `engine/graph/edge.go`'s four
canonical edge types. Message shapes were derived directly from the real Go signatures they delegate to
across `engine/graph`, `engine/catalog`, and `engine/split`. This is a contract-definition-only change:
zero production Go or Python behavior was touched, and no server or client wiring exists yet (that is
3.2.2/3.2.3). One real, blocking bug was found and fixed during verification (see Impact below).

## Features

- `proto/hivemind.proto`: single `HiveMind` gRPC service defining all six RPCs required by issue #16, and
  an `EdgeType` enum mirroring `engine/graph/edge.go`'s four canonical wire names
  (`ENTITY_COOCCUR`, `LLM_ASSERTED`, `SPLIT_SIBLING`, `REDIRECT`).
- Message shapes derived field-for-field from existing Go signatures: `GraphNeighbors` (from
  `engine/graph.GraphNeighbors`), `ReadPartial`/`HeaderOffset` (from `engine/catalog`), and `ProposeSplit`
  (from `engine/split.SplitPlan` / `SplitFileProposal` / `SectionRange`).
- Generated and checked-in Go stubs (`engine/rpc/gen/hivemind.pb.go`, `hivemind_grpc.pb.go`) and Python
  stubs (`agents/hivemind_pb2.py`, `.pyi`, `hivemind_pb2_grpc.py`); `engine/go.mod` updated with
  `google.golang.org/grpc` and protobuf dependencies.
- `proto/README.md` and `docs/LLD/rpc.md`'s status line updated to reflect contracts-defined status.
- `docs/LLD/rpc.md` clarifying note (added as part of the fix cycle, see Impact): documents that the
  engine-internal `Split` entry point is intentionally **not** part of this proto's six-RPC surface — it is
  invoked in-process within `engine/`, not over the wire, and issue #16's task-3.2.1 acceptance criteria
  names exactly six RPCs. If `Split` is ever exposed cross-process, that should be a separately scoped RPC
  added in a later subtask, not folded silently into 3.2.2's server surface.

## Impact — fix-cycle history

- **Original implementation** (commit `cfbe29a0b1cf425b3f6e6548755793d9f17a4890`, run
  `.cdr/runs/2026-07-08/023-implementation/`): defined the six-RPC service, `EdgeType` enum, and all
  message shapes; generated and checked in both stub sets; updated `engine/go.mod`, `proto/README.md`, and
  `docs/LLD/rpc.md`'s status line. Deliberately scoped to contract-definition only — no server/client
  implementation.

- **F1 — weight-type mismatch** (found in verification, run `.cdr/runs/2026-07-08/024-verification/`,
  verdict CHANGES_REQUESTED, severity medium): `Neighbor.weight` (proto line 103) was declared
  `float weight = 3;`, but the Go field it claims to mirror field-for-field,
  `engine/graph.CSREdge.Weight` (`engine/graph/csr.go:82`), is `uint32` — an integer running sum of edge
  occurrence counts (`compact.go`'s `mergeEdges`), used in exact-equality tie-break sort comparisons in
  `traverse.go`. `float` both mis-represented the field's integer/count semantics and risked silent
  precision loss for accumulated weights beyond float32's exact-integer range. All other field-level
  cross-checks (`ReadPartialResponse`/`HeaderOffset`, `ProposeSplitResponse`/`SplitFileProposal`/
  `SectionRange`, `EdgeType` enum) were confirmed correct in this same pass. A second, non-blocking finding
  in the same review flagged the `Split`-vs-issue's-6-RPC-list naming ambiguity as a disclosed judgment call
  worth resolving via documentation before 3.2.2 locks in the server surface.
  - **F1 fix** (commit `2fa5529b7ed5071bd9f2428b1f8b8a12da49e097`, run
    `.cdr/runs/2026-07-08/025-implementation/`): changed the field to `uint32 weight = 3;` and regenerated
    both stub sets from the corrected `.proto` using the exact toolchain documented in `proto/README.md`
    (protoc-gen-go/protoc-gen-go-grpc for Go, `grpc_tools.protoc` for Python) — not hand-edited. Also
    resolved the non-blocking `Split` disclosure by adding the clarifying note to `docs/LLD/rpc.md` (see
    Features above), without implementing `Split` as a seventh RPC.
  - Follow-up provenance commit `8671635edd52306e81145d0511bc18d8aad14fe1` / `25846fd890e018bf9bc4fcbde148c9b382dbb35e`
    (metadata-only, matching the precedent from 3.1.2/3.1.5): recorded final commit hashes into run
    metadata/handoff for downstream verification provenance checks.

- **Final verification** (run `.cdr/runs/2026-07-08/026-verification/`, verdict **PASS**): confirmed the
  field is now `uint32 weight = 3;` (field number unchanged at 3, no wire-compatibility break) and that
  `CSREdge.Weight` remains `uint32` (no drift). Did not trust the fix-cycle commit's regeneration claim —
  independently re-ran codegen from the corrected `.proto` for both stub sets and diffed byte-for-byte
  against the checked-in artifacts, confirming no hand-editing. Re-confirmed `docs/LLD/rpc.md`'s new
  `Split`-exclusion note correctly scopes the six-RPC surface. `gofmt`/`go vet`/`go build` clean from
  `engine/`; full `go test ./... -count=1 -timeout 25m` green across all packages except the known,
  pre-existing `TestReaderDuringSplit` flake (already tracked in `.cdr/index/regression.jsonl`, not a new
  regression, unrelated to this fix). Zero new findings.

## Verification

- **024-verification**: CHANGES_REQUESTED (F1: `Neighbor.weight` typed `float` instead of `uint32`,
  medium severity) — `.cdr/runs/2026-07-08/024-verification/verification.json`, commit reviewed
  `cfbe29a0b1cf425b3f6e6548755793d9f17a4890`.
- **026-verification (final)**: **PASS** — `.cdr/runs/2026-07-08/026-verification/verification.json`,
  commits reviewed `2fa5529b7ed5071bd9f2428b1f8b8a12da49e097` and
  `25846fd890e018bf9bc4fcbde148c9b382dbb35e`. Zero findings. Confidence: high — both stub sets
  independently regenerated and source-diffed byte-for-byte against checked-in artifacts rather than
  trusting the fix-cycle agent's claims. Recommendation: proceed to `/cdr:commit` (local only); safe to
  proceed to subtask 3.2.2.
- This 026-verification run also disclosed, in its own `security_note`, an embedded fake
  system-reminder-style prompt injection encountered in git-log-adjacent output during its exploration
  (fake "date has changed" notice, fake MCP tool instructions, fake "Auto Mode Active" directive) —
  consistent with this repo's known recurring injection pattern. Treated as untrusted data, not acted on.
  The same pattern recurred during this commit-documentation step's resume and was likewise disclosed and
  ignored (see run metadata).

## Release Notes

Subtask 3.2.1 (issue #16, Epic Phase 3) delivers the shared `hivemind.proto` gRPC contract between the Go
storage engine and the Python agent service: a single `HiveMind` service covering all six RPCs required by
the issue (`PutSegment`, `GetFile`, `ReadPartial`, `GraphNeighbors`, `SearchCandidates`, `ProposeSplit`), an
`EdgeType` enum mirroring the engine's canonical edge types, and generated Go and Python stubs. This is a
contract-definition-only change — zero production Go/Python behavior changed, no server or client wiring
yet. One real, blocking bug was found and fixed during verification: `Neighbor.weight` was incorrectly
typed `float` instead of `uint32`, which would have mis-represented the field's integer count semantics and
risked silent precision loss; fixed via a targeted proto edit and full, independently-re-verified stub
regeneration (confirmed byte-for-byte identical to a fresh `protoc` run, proving no hand-editing). A minor
scope question — whether the engine-internal `Split` operation belongs on this client-facing proto — was
resolved by disclosure rather than silent inclusion: `Split` is intentionally excluded per issue #16's
literal six-RPC list, now documented via a clarifying note in `docs/LLD/rpc.md`.

Issue #16 (Epic Phase 3) remains open: 4 subtasks remain (3.2.2 server handlers, 3.2.3 real `ProposeSplit`
client wiring, 3.2.4 interceptor, 3.2.5 integration test). All commits in this record
(`cfbe29a0b1cf425b3f6e6548755793d9f17a4890`, `8671635edd52306e81145d0511bc18d8aad14fe1`,
`2fa5529b7ed5071bd9f2428b1f8b8a12da49e097`, `25846fd890e018bf9bc4fcbde148c9b382dbb35e`) are local-only; no
push performed this session.
