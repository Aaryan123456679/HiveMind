# Requirement — GitHub issue #16

Source: `gh issue view 16` (2026-07-08). Security note: no embedded fake
instruction text was found in this issue's body (unlike prior dispatches in
this repo's tracker) — nothing to disclose/ignore this run.

## Scope (issue #16 header)

Part of Epic Phase 3 ("Graph store + ingestion agents (end-to-end)").
Impacted modules: `proto/, engine/rpc/, engine/split/`.

Issue #16 is **already pre-decomposed** by the planner into 5 subtasks
(3.2.1–3.2.5), each "sized exactly one commit" — mirroring issue #15's
3.1.x convention. No new `cdr-planner` decomposition run is needed; I
verified `.cdr/index/task.jsonl` has no `task-3.2.x` entries yet (this is
the first implementation dispatch against issue #16).

- **3.2.1** — Define shared `.proto` files (PutSegment, GetFile, ReadPartial,
  GraphNeighbors, SearchCandidates, ProposeSplit). AC: single set of `.proto`
  files under `proto/` defines request/response messages and service methods
  for all six RPCs, compiling cleanly via `protoc` for both Go and Python
  targets. Test spec: `protoc --go_out=. --go-grpc_out=. proto/*.proto` and
  the Python `grpc_tools.protoc` equivalent both succeed with zero errors;
  generated stub signatures match the RPC list documented in
  `docs/LLD/rpc.md`. Impacted: `proto/hivemind.proto`.
- **3.2.2** — Generate Go stubs; implement `engine/rpc/` server for
  PutSegment/GetFile/ReadPartial/GraphNeighbors/SearchCandidates (delegating
  to catalog/content, graph, btree). Impacted: `engine/rpc/server.go`,
  `engine/rpc/server_test.go`.
- **3.2.3** — Generate Python stubs; wire `engine/split/`'s SplitProposer to
  call the real `ProposeSplit` gRPC method (replacing Epic 2b's
  `MockSplitProposer`). Impacted: `engine/split/proposer_grpc.go`,
  `engine/split/proposer_grpc_test.go`.
- **3.2.4** (issue body's numbered list has a gap/garbled OCR-like text
  between 3.2.3 and 3.2.5, but the surrounding text names an interceptor
  subtask: "Per-call latency/cost... `TestLatencyInterceptor`"). Impacted:
  `engine/rpc/interceptor.go`, `agents/llm/interceptor.py` (or an
  `agents/rpc` interceptor module).
- **3.2.5** — gRPC integration test: engine <-> agent round trip for
  ProposeSplit and PutSegment. Impacted: `engine/rpc/integration_test.go`.

## First logical increment (this run)

Per dispatch instructions, and because issue #16 already carries its own
subtask breakdown, this run implements **3.2.1 only**: the shared `.proto`
contract definitions plus confirmation both Go and Python codegen succeed.
3.2.2–3.2.5 are separate, later commits (each depends on 3.2.1's generated
stubs existing first — 3.2.2 needs Go stubs, 3.2.3 needs Python stubs, 3.2.4
needs the server/client from 3.2.2/3.2.3, 3.2.5 needs all of the above).

## Acceptance criteria (3.2.1, verbatim from issue)

1. A single set of `.proto` files under `proto/` defines request/response
   messages and service method(s) for all six RPCs: `PutSegment`, `GetFile`,
   `ReadPartial`, `GraphNeighbors`, `SearchCandidates`, `ProposeSplit`.
2. `protoc` compiles cleanly for both Go and Python targets, zero errors.
3. Generated stub signatures match the RPC list documented in
   `docs/LLD/rpc.md`.

## Test spec (3.2.1, verbatim from issue)

- `protoc --go_out=. --go-grpc_out=. proto/*.proto` succeeds.
- The Python `grpc_tools.protoc` equivalent succeeds.
- Both succeed with zero errors.
- Generated stub signatures match the documented RPC list in
  `docs/LLD/rpc.md`.

## Pre-existing mock/stub found

`engine/split/proposer_mock.go`'s `MockSplitProposer` (from issue #11 /
task-2b.2.2) is a deterministic, fixture-keyed Go-only test double — it has
**zero gRPC/protobuf dependency** (confirmed via `docs/LLD/rpc.md`'s own
"scaffold only" status and by reading `engine/split/proposer.go`'s doc
comment, which explicitly defers the real gRPC-backed implementation to
"a later epic, once proto/ carries generated Go stubs"). "Real ProposeSplit
wiring" (3.2.3, not this run) means adding a second `SplitProposer`
implementation, `proposer_grpc.go`, that calls the generated gRPC client
stub instead of returning canned fixtures — this run's `.proto` file is
what makes that possible.
