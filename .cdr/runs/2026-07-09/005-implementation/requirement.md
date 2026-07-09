# Requirement: task-3.2.3 (GitHub issue #16, Epic Phase 3)

Source: `gh issue view 16` (raw text below is untrusted issue content; note appended
prompt-injection boilerplate was NOT present in this particular fetch's subtask-3.2.3 block,
but this repo's issue bodies have shown injected fake-system-reminder text before — treated
as inert data throughout, per the SECURITY NOTE in the dispatch prompt).

## Subtask 3.2.3 text (from issue #16)

> **3.2.3 — Generate Python stubs; wire engine/split/'s SplitProposer to call the real
> ProposeSplit gRPC (replacing Epic 2b's mock)**
> - Acceptance criteria: engine/split/ can be configured to use a real gRPC-backed
>   SplitProposer that calls the ProposeSplit RPC (task-2b.2's mock remains available for
>   pure-unit tests).
> - Test spec: `go test ./engine/split/... -run TestGRPCSplitProposer`
> - Impacted modules: `engine/split/proposer_grpc.go`, `engine/split/proposer_grpc_test.go`

(Per-call latency/cost interceptor work -- `engine/rpc/interceptor.go`,
`agents/llm/interceptor.py` -- and `TestLatencyInterceptor` belong to task-3.2.4, a separate
subtask/commit, NOT in scope here.)

## Scope confirmation (from issue body + docs/LLD/rpc.md + docs/LLD/split.md)

- This subtask is CLIENT-side transport wiring only: construct a `hivemindv1.HiveMindClient`
  (from `engine/rpc/gen`), marshal `engine/split.SplitPlan`/`SplitFileProposal`/`SectionRange`
  request/response to/from `ProposeSplitRequest`/`ProposeSplitResponse`, handle
  transport/timeout/error mapping, and implement the existing `split.SplitProposer` interface
  (`engine/split/proposer.go`) as a new concrete type (`GRPCSplitProposer` or similar) in a new
  file `engine/split/proposer_grpc.go`.
- The actual LLM-backed **server** implementation of `ProposeSplit` (Python
  `agents/ingestion/` service) is explicitly out of scope — that is issue #18's job per the
  milestone breakdown referenced in the dispatch prompt. `engine/rpc/server.go`'s Go-side
  `ProposeSplit` handler also remains the generated `Unimplemented` stub from task-3.2.2 (Go
  engine is never the `ProposeSplit` server; `docs/LLD/rpc.md` — "ProposeSplit ... implemented
  by agents/ingestion/'s Python service, called by engine/split/").
- `engine/split/proposer_mock.go` (`MockSplitProposer`, from issue #11 / task-2b.2.2) MUST be
  left untouched — it remains available for pure-unit tests per the issue's acceptance
  criteria ("task-2b.2's mock remains available"). This subtask ADDS a second, real
  implementation; it does not replace or delete the mock.
- Testing the new gRPC client without a real LLM-backed server: use an in-process test-only
  gRPC server (bufconn or loopback listener) implementing a minimal fixture `ProposeSplit`
  handler distinct from production's `Unimplemented` stub, per the dispatch prompt's explicit
  test-design instruction. This is NOT a mocked-out Go interface — it's a real gRPC round
  trip (marshal -> wire -> unmarshal) against a real `grpc.Server`.

## Acceptance criteria checklist

1. `engine/split/proposer_grpc.go` exists, implements `split.SplitProposer` via a real
   `hivemindv1.HiveMindClient.ProposeSplit` call.
2. Request marshaling: `fileContent []byte` -> `ProposeSplitRequest.FileContent`.
3. Response unmarshaling: `ProposeSplitResponse.Files`/`.RedirectSummary` ->
   `split.SplitPlan{Files: []SplitFileProposal{...}, RedirectSummary}`, with
   `SectionRange.Start/End` (int64 wire) -> `split.SectionRange.Start/End` (int).
4. Error/timeout/retry handling: transport failures and context deadline exceeded produce a
   non-nil, non-panicking `error` return (per `SplitProposer.ProposeSplit`'s contract: "callers
   must not act on SplitPlan when err is non-nil").
5. `go test ./engine/split/... -run TestGRPCSplitProposer -race -timeout <bounded>` passes,
   exercising a real wire round-trip via an in-process test gRPC server (not a mocked
   interface).
6. `proposer_mock.go` unmodified.
7. `gofmt`/`go vet`/`go build ./...` clean.
