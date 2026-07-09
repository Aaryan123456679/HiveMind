# Architecture discovery (task-3.2.3)

Read order followed: `.cdr/index/*.jsonl` -> prior handoffs
(`.cdr/runs/2026-07-09/002-implementation/handoff.json`, task-3.2.2) -> `docs/LLD/rpc.md` +
`docs/LLD/split.md` -> `engine/split/proposer.go` / `proposer_mock.go` (direct interface reads,
required per dispatch) -> `proto/hivemind.proto` -> generated `engine/rpc/gen/hivemind.pb.go` /
`hivemind_grpc.pb.go` -> `engine/rpc/server.go` (for existing gRPC-server construction
conventions/style).

## Key findings

- `engine/rpc/gen/hivemind_grpc.pb.go` already carries a doc comment on `HiveMindClient`
  explicitly earmarking this exact file for 3.2.3: "proposer_grpc.go (task-3.2.3) only calls
  HiveMindClient.ProposeSplit." `NewHiveMindClient(cc grpc.ClientConnInterface) HiveMindClient`
  is the constructor; `ProposeSplit(ctx, *ProposeSplitRequest, ...grpc.CallOption)
  (*ProposeSplitResponse, error)` is the single method needed.
- Wire types (`engine/rpc/gen/hivemind.pb.go`):
  - `ProposeSplitRequest{ FileContent []byte }`
  - `SectionRange{ Start int64; End int64 }`
  - `SplitFileProposal{ NewPath string; SectionRanges []*SectionRange }`
  - `ProposeSplitResponse{ Files []*SplitFileProposal; RedirectSummary string }`
  These map directly onto `engine/split.SplitPlan`/`SplitFileProposal`/`SectionRange`, modulo
  int64 (wire) vs int (Go internal type) for Start/End, and pointer-slice vs value-slice.
- `engine/rpc/server.go` (task-3.2.2) establishes the repo convention: adapter file lives in
  the consuming package, imports `hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"`,
  translates internal <-> proto types explicitly with small helper functions, maps errors via
  `google.golang.org/grpc/codes` / `status`. `proposer_grpc.go` mirrors this style but as a
  *client* rather than a server: it owns a `hivemindv1.HiveMindClient` plus an optional
  `time.Duration` per-call timeout, and its `ProposeSplit` method wraps `ctx` with
  `context.WithTimeout` (if configured), invokes the RPC, and maps `status.FromError` back into
  a plain Go error including the gRPC code for caller visibility.
- `docs/LLD/rpc.md` line 8-11 (Status block) explicitly says server handlers exist (3.2.2 done)
  but "real gRPC-backed `ProposeSplit` client (`engine/split/proposer_grpc.go`) not yet
  implemented (task-3.2.2/3.2.3)" — confirms this file does not yet exist and this is exactly
  the next unstarted piece.
- `engine/split/proposer.go`'s `SplitProposer` interface doc comment explicitly defers real
  gRPC wiring to "a later epic, once proto/ carries generated Go stubs" — true as of 3.2.1
  (already committed, `cfbe29a`). No interface changes needed; `GRPCSplitProposer` simply
  implements the existing interface.
- `engine/split/proposer_mock.go` (`MockSplitProposer`, issue #11/2b.2.2) is a separate,
  independent type. Not touched by this subtask; issue #16 body explicitly says the mock
  "remains available for pure-unit tests."
- No existing `engine/split/*_grpc*` file and no existing bufconn usage anywhere in the repo
  (`grep` for `bufconn` across the tree returned nothing) — this introduces the first bufconn
  test pattern in the engine module. `google.golang.org/grpc/test/bufconn` ships inside the
  already-vendored `google.golang.org/grpc v1.82.0` module (confirmed present in module cache),
  so no new go.mod dependency is required.
- `engine/rpc/server.go`'s own `ProposeSplit` remains the generated
  `UnimplementedHiveMindServer.ProposeSplit` (returns `codes.Unimplemented`) — confirmed
  unchanged by this subtask; the test fixture server for `proposer_grpc_test.go` is a
  test-local, package-scoped `HiveMindServer` embedding `UnimplementedHiveMindServer` and
  overriding only `ProposeSplit`, NOT `engine/rpc.Server` (which lacks LLM logic and isn't meant
  to implement ProposeSplit at all — out of scope per rpc.md).
