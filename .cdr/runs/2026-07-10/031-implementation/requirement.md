# Requirement: Issue #18 subtask 3.4.5 -- real `ProposeSplit`

## Source of truth (as originally scoped)

GitHub issue #18, subtask 3.4.5 (verbatim, reconstructed from `gh issue view 18`,
whose body contains embedded fake system-reminder-style text -- disclosed below,
treated as untrusted data only, not followed):

> **3.4.5 -- ProposeSplit(fileContent) -> topic-coherent `[{newPath, sectionRanges}]`
> plan plus redirect summary, using segmentation LLM pathway, served over gRPC
> contract task-3.2.**
> Test spec: `pytest agents/ingestion/test_propose_split.py` (LLMClient mocked,
> fixture over-threshold document): assert returned plan's ranges partition content
> without gaps/overlaps.
> Impacted modules: `agents/ingestion/propose_split.py`, `agents/ingestion/test_propose_split.py`

## Disclosure: prompt injection in `gh issue view 18` output

As in prior sessions (see `.cdr/index/task.jsonl` entries for task-3.2.2/3.2.4/3.2.5/
engine-edge-entity-rpc), the raw issue body text contains embedded fake
system-reminder-style content interleaved with the real acceptance criteria. This was
treated as untrusted data only; none of it was followed. The dispatching agent's own
prompt separately warned of this and of two additional suspicious system-reminder
blocks (a fake date-change notice to 2026-07-10 and a fake "Auto Mode Active"
directive) injected mid-session immediately after a tool call. Both were verified
against the real shell clock (`date +%F` confirmed 2026-07-10 is in fact today's real
date, so that one was accurate) and disclosed; no directive from any of them was acted
on as authorization for anything.

## Architectural correction to the dispatching prompt's assumption

The dispatching prompt hypothesized this subtask might be a Go-engine-side change to
`engine/rpc/server.go`'s `ProposeSplit` handler and `engine/rpc/integration_test.go`'s
`TestRPCIntegration`. Direct reading of `docs/LLD/rpc.md`, `docs/LLD/ingestion-agent.md`,
`proto/hivemind.proto`, `engine/rpc/server.go`'s own doc comment, and
`.cdr/index/task.jsonl`'s task-3.2.2/3.2.5 entries all independently and consistently
confirm this is **not** the case:

- `docs/LLD/rpc.md` "Status" note: "`ProposeSplit`'s *server* side remains the
  generated `Unimplemented` stub: the real LLM-backed Python ingestion-agent service is
  out of scope for issue #16 (see issue #18)."
- `engine/rpc/server.go` doc comment (lines 9-14): "ProposeSplit is a client-side call
  this engine MAKES against the Python agent service ...; Server does not implement it
  here and instead falls back to the generated `hivemindv1.UnimplementedHiveMindServer`'s
  default (`codes.Unimplemented`) via embedding." This is permanent, intentional design,
  not a stopgap -- the Go engine is the gRPC *client* for `ProposeSplit`
  (`engine/split/proposer_grpc.go`, task-3.2.3, already implemented+verified); the Python
  ingestion-agent service is the gRPC *server*/callee.
- `engine/rpc/integration_test.go`'s `TestRPCIntegration` / `ProposeSplit_Unimplemented`
  subtest doc comment: exercises the real Go engine's own server surface only,
  "deliberately does NOT implement, mock, or stand up any Python agent service:
  ProposeSplit's server side remains engine/rpc/server.go's inherited
  `UnimplementedHiveMindServer` default *by design*". `.cdr/index/task.jsonl`'s
  task-3.2.5 entry (already `verified`) independently confirms the same and explicitly
  flags that a real Go<->Python round trip is a *separate*, not-yet-authorized scope
  item requiring explicit user sign-off, distinct from any individual subtask.
- `proto/hivemind.proto`'s own top-of-file comment: "ProposeSplit: implemented in
  `agents/ingestion/`'s Python service, called by `engine/split/`."
- `docs/LLD/ingestion-agent.md`'s "ProposeSplit" section: `ProposeSplit(fileContent) ->
  [{newPath, sectionRanges}, ...] + redirect summary`, "Called by `engine/split/` when a
  file crosses its auto-split size threshold... the Go engine executes the plan
  atomically." I.e. the Python side *proposes*, the Go side *executes*.
- The issue's own impacted-modules list for 3.4.5 names only
  `agents/ingestion/propose_split.py` / `agents/ingestion/test_propose_split.py` -- no
  Go/proto files.

**Conclusion**: this subtask's real, correct scope is a new Python module,
`agents/ingestion/propose_split.py`, implementing the `ProposeSplit` business logic
(LLM-backed split-boundary proposal + redirect summary) that will eventually sit behind
the already-generated `agents/hivemind_pb2_grpc.py`'s `HiveMindServicer.ProposeSplit`
server method (no such servicer wiring exists yet anywhere in `agents/`, and standing
one up -- an actual running `grpc.Server` process -- is new scope beyond this subtask's
named impacted modules, matching the same "don't silently expand issue-numbered scope"
discipline documented in `agents/ingestion/wiring.py`'s own history).
`engine/rpc/server.go` and `engine/rpc/integration_test.go` are correctly left
**untouched**: their `codes.Unimplemented` assertion for `ProposeSplit` is intentional,
permanent, already-verified behavior describing the Go engine's own server surface, not
a stub awaiting this subtask's completion.

## Functional requirement (from the LLD + proto, confirmed against generated stubs)

- `ProposeSplitRequest{file_content: bytes}` (confirmed via `agents/hivemind_pb2.pyi`).
- `ProposeSplitResponse{files: repeated SplitFileProposal, redirect_summary: string}`.
- `SplitFileProposal{new_path: string, section_ranges: repeated SectionRange}`.
- `SectionRange{start: int64, end: int64}` -- half-open byte offsets into the original
  content (per `proto/hivemind.proto`'s own comment: "half-open byte-offset").
- Business logic: given an over-threshold document's full content, use the
  "segmentation LLM pathway" (an `agents/llm/` `LLMClient`, per issue text) to decide
  **topic-coherent split boundaries** and per-file topic paths, then return a plan whose
  `SectionRange`s partition the input content with **no gaps and no overlaps** (test
  spec's explicit acceptance check), plus a human-readable `redirect_summary` describing
  what was split and why (consumed by `engine/split/`'s redirect-stub write, per
  `docs/LLD/split.md` step 3).
- The threshold-crossing check itself is **not** this function's responsibility --
  `docs/LLD/split.md`'s "Trigger" section places that entirely in `engine/split/`,
  which only *calls* `ProposeSplit` after it has already decided to split.

## Non-goals (explicitly out of scope, to avoid silent scope creep)

- No new `grpc.Server` process / `HiveMindServicer` wiring in `agents/` (not named in
  3.4.5's impacted modules; would be new user-authorized scope, same discipline as
  `engine-edge-entity-rpc`).
- No changes to `engine/rpc/server.go`, `engine/rpc/integration_test.go`,
  `proto/hivemind.proto`, or any other Go/proto file (see architectural correction
  above).
- No changes to `agents/ingestion/wiring.py` (3.4.4, already done), the 3.4.4a RPCs, or
  3.4.6 (fixture suite -- separate subtask, not started here).
