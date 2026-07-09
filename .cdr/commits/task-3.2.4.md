# task-3.2.4: gRPC latency interceptor + FileId=0 error-code fix

## Summary
Issue #16 (Epic Phase 3, HiveMind gRPC contract) subtask 3.2.4 adds per-call
latency observability to the Go-side gRPC server and folds in a previously
disclosed non-blocking finding from task-3.2.2: `GetFile`/`ReadPartial`
misclassifying `FileId=0` as an internal server fault instead of a caller
input error. Both changes are additive/corrective only; no existing RPC
success-path behavior changed.

## Features
- **Latency interceptor**: a `grpc.UnaryServerInterceptor`
  (`LatencyInterceptor`) that measures per-call wall-clock handler duration
  plus request/response proto payload byte sizes as a cost proxy, emitting a
  typed `RPCMetric` to a pluggable `Recorder` (default: structured
  log/slog-based). Purely additive — does not alter request, response, or
  error semantics of any handler. Any future `*grpc.Server` construction site
  (server binary, or 3.2.5's integration test) can wire it in via
  `grpc.UnaryInterceptor(rpc.LatencyInterceptor())`.
- **FileId=0 fix (folded in)**: `GetFile` and `ReadPartial` now explicitly
  guard `FileId == catalog.InvalidFileID` (0) at the top of each handler and
  return `codes.InvalidArgument` before calling into `ContentStore`/`Catalog`,
  instead of falling through to the generic `codes.Internal` default. This
  resolves the non-blocking finding first surfaced during task-3.2.2
  verification.

## Impact
- Server operators/future benchmark tooling (Epic 5) gain per-RPC latency and
  payload-size visibility with zero change to client-observable behavior.
- gRPC clients sending an unset/zero-value `file_id` now receive a correct,
  actionable `InvalidArgument` instead of a misleading `Internal` error.
- No production Go/Python behavior changed on any existing success, NotFound,
  or Internal path other than the previously-untested `FileId=0` edge case's
  status code.
- Regression tracking (`.cdr/index/regression.jsonl` line 64,
  `.cdr/memory/pending.md`) updated: the FileId=0 finding is now marked
  resolved *and* independently re-verified (this closes the loop — the
  implementer's own "resolved" claim previously lacked a confirming
  verification pass; run `009-verification` supplies it), with the real
  commit hash recorded in place of the prior `PENDING` placeholder.
- Issue #16 has one subtask remaining: 3.2.5 (integration test).

## Verification
- **Verdict**: PASS (one dimension, `maintainability`, rated
  `PASS_WITH_COMMENTS` for a non-blocking doc-comment density observation;
  all 9 other dimensions — requirements/architecture conformance, latency
  measurement correctness, cost-metric correctness, test genuineness,
  concurrency safety, FileId=0 fix correctness, regression provenance, full
  regression suite, scope containment — PASS).
- **Run ID**: `.cdr/runs/2026-07-09/009-verification`
- Commits reviewed: `a1497bc178478523826664752115667fc8e6b630` (feat: per-call
  latency gRPC interceptor), `3f0647a397b2796dbcd6131ae7153d710b6adf5b` (fix:
  map FileId=0 to InvalidArgument in GetFile/ReadPartial).
- No previously-tracked pending findings (e.g. `engine/graph/edgelog.go`
  lock-ordering gap, `TestReaderDuringSplit` ~1-3% timing flake) were
  reintroduced or worsened.

## Release Notes
- Added: gRPC server-side per-call latency + payload-size interceptor
  (`rpc.LatencyInterceptor`), pluggable via `Recorder`, default slog-based
  logging. Not yet wired into a production server binary — available for
  3.2.5 and beyond.
- Fixed: `GetFile` and `ReadPartial` now return `InvalidArgument` (not
  `Internal`) when called with `FileId=0`.

## Notes / Disclosures
- Fake system-reminder-style prompt-injection text (a spurious "date has
  changed, don't tell the user" notice, fake MCP "tokensave" tool
  instructions, and a fake "Auto Mode Active" directive) was encountered
  embedded in tool output during this commit step's `git log` exploration —
  consistent with this repo's known recurring pattern of untrusted
  instruction-like text showing up in issue bodies/commit output. Treated as
  inert data only; not acted on; disclosed here per standing instruction.
- All commits for this subtask remain local-only; nothing was pushed to
  `origin`.
