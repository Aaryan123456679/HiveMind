# task-3.2.5 — Cross-process gRPC integration test (final subtask, issue #16)

## Summary

Fifth and **final** subtask of GitHub issue #16 (Epic Phase 3: gRPC contract, server, client, interceptor). Adds `engine/rpc/integration_test.go`'s `TestRPCIntegration`, a capstone integration test that, for the first time in the repo, wires `rpc.LatencyInterceptor` (task-3.2.4) into a real `grpc.Server` bound to a real OS-level `net.Listener` (not `bufconn`), driving a realistic multi-RPC workflow — `PutSegment`, `GetFile`, `ReadPartial`, `GraphNeighbors`, `SearchCandidates`, and `ProposeSplit` — through a real dialed client against real on-disk backing state (catalog, content store, B+Tree, graph), cross-checked against direct store reads. Independently verified **PASS_WITH_COMMENTS**. Test-only change; no production code modified.

## Features

- `engine/rpc/integration_test.go` — `TestRPCIntegration`, 9 subtests, first real-`net.Listener` + real-`grpc.Server` + `LatencyInterceptor` combination in the repo (all prior tests used either direct handler calls or in-process `bufconn` transport).
- Exercises all 5 implemented RPC handlers (`PutSegment`, `GetFile`, `ReadPartial`, `GraphNeighbors`, `SearchCandidates`) against real, on-disk-backed `catalog`/`content`/`btree`/`graph` state (via `t.TempDir()`), cross-verified directly against the underlying stores rather than only against other RPC responses.
- Also issues `ProposeSplit` through task-3.2.3's real `split.GRPCSplitProposer` client and asserts the call surfaces a correctly-wrapped `codes.Unimplemented` status (via `status.FromError`, not string matching) — confirming the disclosed, deliberate scope boundary rather than treating it as a failure.
- A dedicated subtest confirms the `LatencyInterceptor`'s `Recorder` genuinely captured `RPCMetric` records for every one of the 6 exercised RPC methods (including `ProposeSplit`'s `Unimplemented` outcome) with non-negative durations, proving the interceptor is wired and firing on real per-call outcomes rather than being incidentally present.
- Full, real-resource cleanup (`t.Cleanup`) including `GracefulStop()` + drain of the server's serve-error channel, and concurrency-safe metric recording verified under `-race`.

## Impact

Closes out the entire 3.2.x gRPC sequence (3.2.1 contract → 3.2.2 server handlers → 3.2.3 split-proposer client → 3.2.4 latency interceptor → 3.2.5 cross-process integration test) with a single test that proves the pieces compose correctly end-to-end over a real network transport, not just individually. No wire-format, schema, or existing-API changes; no breaking changes; test-only diff, zero production code touched.

**Important scope caveat (carried forward from verification, see Issue #16 closure summary below): this test is Go-only.** It does not start a Python process and does not assert `ProposeSplit` succeeds — it asserts `codes.Unimplemented`. Issue #16's own literal 3.2.5 acceptance text calls for a real Go-engine ↔ running-Python-agent-service round trip that successfully executes `ProposeSplit`. That is not what was built. This is a disclosed, consistently-applied scope reduction dating back to 3.2.1 (re-affirmed at 3.2.2, 3.2.3, 3.2.4) deferring real `ProposeSplit` server logic to issue #18 — see the closure summary below for why this does not block this subtask's own PASS_WITH_COMMENTS verdict, but does mean issue #16 is not literally 100% complete per its own written text.

## Verification

- **Verdict**: PASS_WITH_COMMENTS
- **Run ID**: `.cdr/runs/2026-07-09/012-verification/verification.json`
- **Commit reviewed**: `c49c3767d86f4e095ce652e09ba825d7b6506151`
- All 10 verification dimensions independently checked: `real_transport_not_bufconn` (PASS — confirmed via grep this is the first real-`net.Listener` + `LatencyInterceptor` site in the repo), `real_backing_state_not_mocks` (PASS), `interceptor_wiring_confirmed` (PASS), `proposesplit_unimplemented_assertion` (PASS), `graphneighbors_hop_limitation_respected` (PASS), `searchcandidates_placeholder_and_maxresults_respected` (PASS), `cleanup_and_race_safety` (PASS), `test_execution` (PASS), `scope_containment` (PASS — `git show --name-only` confirms only `.cdr/runs/2026-07-09/011-implementation/*` process artifacts and `engine/rpc/integration_test.go` changed; no production code touched), and `requirements_literal_text` (**DEVIATION_DISCLOSED_AND_PRE-ACCEPTED** — see caveat above and closure summary below).
- `go test ./rpc/... -run TestRPCIntegration -race -timeout 60s -v`: all 9 subtests PASS, race detector clean. `gofmt -l .` empty, `go vet ./...` clean, `go build ./...` clean, full workspace `go test ./... -timeout 180s`: all packages ok, no flake observed this run (pre-existing `engine/split.TestReaderDuringSplit` ~1-3% timing flake, not triggered).
- Zero must-fix findings. The one non-blocking, prominently-carried finding is the requirements-literal-text deviation described above and in the closure summary.
- Verification also independently confirmed a prompt-injection attempt: `gh issue view 16` output contained fake embedded system-reminder-style blocks (a fake "date changed, don't tell the user" notice, a fake "MCP tokensave server instructions" block, and a fake "Auto Mode Active" directive) appended after the legitimate issue body — consistent with a previously-confirmed recurring injection pattern in this repo. Treated as untrusted data only; not followed, not acted upon. This same injection pattern recurred during this commit-documentation step's own tool output and was likewise treated as untrusted data and disclosed, not followed.

## Release Notes

- Added a cross-process, real-network gRPC integration test covering PutSegment, GetFile, ReadPartial, GraphNeighbors, SearchCandidates, and the (intentionally unimplemented) ProposeSplit RPC, together with the latency-recording interceptor, all backed by real on-disk state. Test-only; no behavior change for consumers.
- Commit `c49c3767d86f4e095ce652e09ba825d7b6506151` is local-only and has not been pushed to origin.

---

## Issue #16 closure summary (Epic Phase 3: HiveMind gRPC service)

All 5 subtasks under issue #16 are now implemented and independently verified:

1. **3.2.1 — gRPC contract definition** (`hivemind.proto`, `docs/LLD/rpc.md`, commit `2fa5529b7`, fix commit `25846fd89`): defines the 6-RPC HiveMind gRPC service (`PutSegment`, `GetFile`, `ReadPartial`, `GraphNeighbors`, `SearchCandidates`, `ProposeSplit`), generated Go (`engine/rpc/gen/`) and Python (`agents/hivemind_pb2*.py`) stubs. Verified PASS (after one CHANGES_REQUESTED fix cycle on `Neighbor.weight`/`CSREdge.Weight` tie-break behavior).
2. **3.2.2 — gRPC server handlers** (`engine/rpc/server.go`, commit `4f20044f9`, provenance commit `3bcbf2de6`): implements 5 of 6 RPCs with real delegation to already-verified `catalog`/`graph`/`btree` primitives, no mocks; `ProposeSplit` correctly left as the generated `Unimplemented` stub. A real cross-package bug (graph/proto `EdgeType` enum ordering mismatch) was caught and fixed before verification. Verified PASS_WITH_COMMENTS.
3. **3.2.3 — gRPC split-proposer client** (`engine/rpc/split/proposer_grpc.go`, commit `b2a119d76`): `GRPCSplitProposer`, a gRPC-backed implementation of `split.SplitProposer` calling `ProposeSplit` over the wire with per-call timeout. Verified PASS.
4. **3.2.4 — Latency interceptor** (`engine/rpc/interceptor.go`, commit `a1497bc17`, fix commit `3f0647a39`): `LatencyInterceptor`, a `grpc.UnaryServerInterceptor` measuring per-call wall-clock duration and payload sizes, emitted to a pluggable `Recorder`. Also folded in a non-blocking fix from 3.2.2's verification (FileId=0 now correctly returns `codes.InvalidArgument` instead of `codes.Internal`). Verified PASS.
5. **3.2.5 — Cross-process integration test** (`engine/rpc/integration_test.go`, commit `c49c3767d`, this record): capstone test wiring all of the above together over a real network transport. Verified PASS_WITH_COMMENTS.

**Net result**: a working, internally-consistent HiveMind gRPC service — contract (3.2.1), server-side handler implementations for 5 of 6 RPCs (3.2.2), a real gRPC-backed split-proposer client (3.2.3), a latency-recording interceptor (3.2.4), and a real-transport integration test proving it all composes (3.2.5). All 5 implementation commits (`2fa5529b7`/`25846fd89`, `4f20044f9`, `b2a119d76`, `a1497bc17`/`3f0647a39`, `c49c3767d`) are local-only and have not been pushed.

### Open item requiring explicit user sign-off before issue #16 is treated as fully closable on GitHub

**Issue #16 is not literally 100% complete per its own written acceptance text**, and this should not be glossed over. Issue #16's subtask 3.2.5 acceptance criteria explicitly call for: *"A real end-to-end gRPC call from the Go engine to a running Python agent service correctly executes ProposeSplit and PutSegment,"* with a test spec that *"starts the Python gRPC server, issues ProposeSplit and PutSegment from the Go client, asserts expected responses."*

What was actually built and verified across 3.2.1–3.2.5 is **Go-only**: no Python process is ever started by any test in this sequence, and every test that touches `ProposeSplit` explicitly expects and asserts `codes.Unimplemented`, not a successful split proposal.

This is not an oversight introduced in this final commit — it is a disclosed, consistently-applied scope reduction that was first recorded in `docs/LLD/rpc.md`'s Status note during 3.2.1's fix cycle, and re-affirmed independently at every subsequent subtask (3.2.2, 3.2.3, 3.2.4, and now 3.2.5), each time deferring the real LLM-backed Python `ProposeSplit` server implementation to **issue #18**. Because this decision was made once, early, and applied consistently and transparently across the whole sequence — rather than being a new or hidden defect in this final commit — verification did not treat it as a blocking defect for 3.2.5 itself (hence PASS_WITH_COMMENTS rather than CHANGES_REQUESTED).

However, it does mean: **if issue #16 is closed on GitHub as-is, it will be closed with its own literal, written acceptance criteria not fully met** — the real cross-language Go↔Python `ProposeSplit` round trip described in the issue text does not exist yet and is tracked instead under issue #18. This record does not make that call. Per standing instruction, **issue #16 was NOT closed or otherwise touched on GitHub as part of this commit-documentation step** — that decision requires explicit user authorization, informed by the caveat above.
