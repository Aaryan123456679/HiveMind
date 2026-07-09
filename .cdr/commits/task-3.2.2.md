# task-3.2.2 — `engine/rpc/server.go` HiveMind gRPC server handlers (issue #16, Epic Phase 3)

## Summary

Subtask 3.2.2 ("implement the Go-side gRPC server handlers for the shared `hivemind.proto` contract")
is complete and independently verified. This is the second increment of issue #16's five-part gRPC
rollout (3.2.1-3.2.5): `engine/rpc/server.go` implements 5 of the 6 `HiveMind` service RPCs defined in
3.2.1's contract, delegating each directly to the real, already-verified storage-engine primitives it
wraps — no mocks, no stubbed business logic. `ProposeSplit` is correctly left as the generated
`Unimplemented` stub, deferred to task-3.2.3 (real client wiring) per issue #16's own increment
boundaries. One real, non-blocking (but previously latent) cross-package correctness bug was caught and
fixed during implementation — see Impact below.

## Features

- `PutSegment` RPC: delegates to `catalog.ContentStore.Create` (new file) or `.Append` (existing file),
  branching on caller intent.
- `GetFile` RPC: composes `catalog.ContentStore.Read` with `catalog.Catalog.Get` for file metadata.
- `ReadPartial` RPC: delegates to `catalog.ContentStore.ReadPartial` for header-offset-based partial reads.
- `GraphNeighbors` RPC: delegates to `engine/graph.GraphNeighbors`, using new explicit,
  name-based `protoEdgeTypeToGraph`/`graphEdgeTypeToProto` conversion helpers (see Impact) rather than a
  naive integer cast.
- `SearchCandidates` RPC: delegates to `engine/btree.PrefixScan`.
- `ProposeSplit` RPC: intentionally left as the protoc-generated `Unimplemented` stub; real wiring is
  scoped to task-3.2.3.
- `mapCatalogError`: centralized error-to-gRPC-status mapping shared across handlers.
- New `engine/rpc/server_test.go`: `TestRPCServerHandlers` (15 subtests covering happy paths, not-found
  paths, input-validation paths, and a concurrent-access subtest) plus a dedicated
  `TestEdgeTypeConversionRoundTrip`. All fixtures are real (composed from `engine/catalog`,
  `engine/graph`, `engine/btree` objects mirroring `engine/integration_test.go`'s composition style); no
  mocks anywhere in the suite.
- Disclosed, real (not silently-swallowed) scope limitations, all confirmed genuine rather than
  oversights during verification:
  - `Neighbor.hop` is always returned as `0` — `graph.GraphNeighbors`' return type does not currently
    expose hop distance from the traversal origin.
  - `CandidateTopic.score` is a placeholder constant — no relevance-scoring primitive exists anywhere in
    the engine yet.
  - `PutSegment` cannot populate `PathHash` or insert into the B+Tree index — the current proto message
    has no path field to derive one from.

## Impact

- **EdgeType enum cross-package mismatch bug (found and fixed during implementation, before any
  verification pass)**: `graph.EdgeType`'s Go `iota`-based declaration order in
  `engine/graph/edge.go` does not numerically match the wire values of the proto `EdgeType` enum defined
  in 3.2.1's `proto/hivemind.proto`. A naive `graph.EdgeType(int(protoEdgeType))`-style integer cast —
  the obvious first-pass implementation for bridging the two enums across the RPC boundary — would have
  compiled cleanly, passed any test that only checks a single edge type in isolation, and then silently
  mismapped edge types on every real `GraphNeighbors` RPC response in production (e.g. an
  `ENTITY_COOCCUR` edge on the wire being reported to a Python agent-service caller as `LLM_ASSERTED`, or
  vice versa) — a correctness bug with no crash, no error, and no test failure to flag it, only silently
  wrong data reaching callers. Fixed via explicit, named `protoEdgeTypeToGraph`/`graphEdgeTypeToProto`
  conversion functions (`engine/rpc/server.go`) that switch on symbolic names rather than raw integer
  values. Independently re-derived and confirmed correct for every valid enum value, plus explicitly
  tested for out-of-range/invalid inputs on both directions, by the verification pass
  (`edge_type_conversion_correctness` dimension, `.cdr/runs/2026-07-09/003-verification/verification.json`)
  via `TestEdgeTypeConversionRoundTrip` and independent source cross-referencing of both enum
  declarations. This closes the same general risk class already flagged (but for a different code path)
  in `.cdr/memory/pending.md`'s `LoadCSR`/`decodeCSREdge` on-disk-byte-validation item from task-3.1.1 —
  cross-boundary `EdgeType` representation mismatches are a recurring hazard in this codebase and worth
  keeping in mind for any future third representation (e.g. a REST/JSON API) of the same enum.
- Zero production behavior in `engine/catalog`, `engine/graph`, or `engine/btree` was touched — this is
  a pure delegation/wiring layer. All underlying primitives were already independently verified in prior
  subtasks (issue #15, #12, task-3.2.1).
- **One non-blocking finding surfaced during verification, deliberately not fixed here** (recommended by
  verification to fold into 3.2.4 or a small standalone follow-up, out of scope for 3.2.2): `GetFile`
  and `ReadPartial`, when called with `FileId=0` (proto3's zero-value default for an unset scalar field,
  as opposed to a genuinely nonexistent nonzero `FileId`), return `codes.Internal` instead of a proper
  client-error code (`codes.InvalidArgument` or similar). This happens because `catalog.Catalog.Get`
  does not special-case `FileID==0` (`catalog.InvalidFileID`) as distinct from a real not-found lookup —
  an ordinary client-side mistake (forgetting to set `FileId`, or a mis-marshaled request) is
  misclassified as an internal server fault rather than a client error. Untested and un-fixed by this
  subtask. Recorded in `.cdr/index/regression.jsonl` and `.cdr/memory/pending.md` with a forward
  reference to GitHub milestone #10 (Phase 4.5: storage-engine technical debt & correctness follow-ups,
  issues #38-42), matching this repo's standing convention for non-blocking findings surfaced during
  Phase 3 work.

## Verification

- **003-verification**: **PASS_WITH_COMMENTS** — `.cdr/runs/2026-07-09/003-verification/verification.json`,
  commit reviewed `4f20044f93f45de078307393d6503e1b05d4e3f5`, provenance commit
  `3bcbf2de60b7e5c5b3bea1cc306a9218c1690db6`. All 11 dimensions passed (`requirements_conformance`,
  `architecture_conformance`, `edge_type_conversion_correctness`, `put_segment_branch_logic`,
  `search_candidates_scope_honesty`, `concurrency`, `test_genuineness`, `regression_risk`,
  `maintainability`, `provenance` all `pass`; `error_mapping` `pass_with_comment` for the `FileId=0`
  finding above). No blocking findings. `TestEdgeTypeConversionRoundTrip` and the `edge_type_conversion`
  bug-catch were independently re-derived and confirmed correct, not merely trusted from the
  implementer's own claim.
- This verification run also disclosed, in its own `prompt_injection_observed` field, embedded fake
  system-reminder-style prompt injection encountered in `gh issue view 16` raw tool output during its
  exploration (fake "date has changed" notice, fake "tokensave" MCP tool instructions, fake "Auto Mode
  Active" directive) — consistent with this repo's known recurring injection pattern, and the same
  pattern re-observed and pre-flagged by the implementer itself in
  `.cdr/runs/2026-07-09/002-implementation/requirement.md`. Treated as untrusted data, not acted on. The
  identical injection pattern recurred again during this commit-documentation step's own exploration
  (`git log` output) and was likewise ignored, not acted on.

## Release Notes

Subtask 3.2.2 (issue #16, Epic Phase 3) delivers the Go-side gRPC server handlers for 5 of the 6
`HiveMind` RPCs defined in 3.2.1's shared contract (`PutSegment`, `GetFile`, `ReadPartial`,
`GraphNeighbors`, `SearchCandidates`), each delegating directly to already-verified storage-engine
primitives with no mocks; `ProposeSplit` remains the generated `Unimplemented` stub pending task-3.2.3.
A real cross-package correctness bug — a numeric mismatch between `graph.EdgeType`'s Go declaration
order and the proto `EdgeType` enum's wire values, which would have silently mismapped edge types on
every `GraphNeighbors` response — was caught and fixed via explicit name-based conversion functions
before any code shipped, and independently re-confirmed correct by verification. One non-blocking gap
(`FileId=0` misclassified as `codes.Internal` instead of a client error) is disclosed, tracked in
`.cdr/index/regression.jsonl` and `.cdr/memory/pending.md`, and deliberately deferred rather than fixed
in this subtask.

Issue #16 (Epic Phase 3) remains open: 3 subtasks remain (3.2.3 real `ProposeSplit` client wiring, 3.2.4
gRPC interceptor, 3.2.5 integration test).
