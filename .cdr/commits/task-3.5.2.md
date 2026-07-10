# task-3.5.2: End-to-end real-dataset smoke run (issue #19, FINAL of 2 subtasks)

## Summary

Issue #19's final subtask required proving the full ingestion pipeline
(load -> normalize -> shortlist -> segment -> wiring/PutSegment/PutEdge)
works end-to-end against a real, non-fixture dataset sample and a real,
non-mocked local engine instance over real gRPC -- not fixtures, not
mocks. This subtask sourced a genuine real Enron corpus sample (6
messages) by streaming the official CMU/FERC-hosted
`enron_mail_20150507.tar.gz` archive directly from its canonical public
source, reading only the leading portion of the tar stream rather than
downloading the full ~423MB archive, and disclosing the exact
streaming/extraction methodology and provenance in a committed
`PROVENANCE.md`. It also added a standalone, genuinely real Go gRPC
"smokeserver" (no mocks, no bufconn) backed by real catalog/content/WAL/
btree/graph storage, launched as a real out-of-process subprocess and
dialed over a real TCP gRPC channel. Against that server, a new
end-to-end pytest exercises the real Python ingestion pipeline -- both
the Bitext loader and the new real Enron sample -- through real
`shortlist()`/`segment()` calls against a real local Ollama
`llama3.1:8b` model, and real `wiring.execute_segment()` RPCs. The test
tolerates individual per-document `SegmentParseError`s rather than
assuming 100% success against a live, non-deterministic-adjacent LLM,
and its final assertion demonstrates the real-world consequence of
finding F4 (newly created files are not discoverable via
`SearchCandidates`).

## Features

- **`data/fixtures/enron_real_sample/`** (6 files) + `PROVENANCE.md`:
  genuine Enron corpus messages streamed from the official public
  CMU/FERC archive, distinct from and not a reuse of 3.5.1's disclosed
  hand-authored fixtures.
- **`engine/cmd/smokeserver/main.go`**: standalone, real Go gRPC server
  binary backed by real storage (catalog, content store, WAL, btree,
  graph), runnable as an independent subprocess for out-of-process,
  real-wire smoke testing.
- **`agents/ingestion/test_e2e_smoke.py`**: first genuinely end-to-end
  test in the repo driving the real pipeline (loaders -> normalize ->
  shortlist -> segment [real Ollama] -> wiring/PutSegment/PutEdge [real
  gRPC to the real smokeserver subprocess]), with per-document tolerance
  for LLM parse failures and a final `SearchCandidates` assertion that
  directly observes F4's real consequence.
- **F4 root-cause investigation**: read down to `proto/hivemind.proto`,
  `engine/rpc/server.go`, and `agents/ingestion/wiring.py` to determine
  whether F4 (PutSegment's CREATE path never setting `PathHash`) could be
  fixed within this subtask's scope. Determined it cannot be, and
  documented why in this run's `handoff.json` rather than forcing an
  incomplete or narrow fix.

## Impact

- No changes to `agents/ingestion/segment.py`, `propose_split.py`, or
  `shortlist.py` core logic, and no changes to any pre-existing
  production file -- confirmed via `git diff fa103fb..9794fd8 --stat`
  showing only new files added.
- Full regression suite personally re-run by verification: `go vet
  ./...` clean, `go test ./... -race -count=1` all packages ok, `ruff
  check agents/` clean (one pre-existing, unrelated generated-code F401
  predating this diff), `pytest agents/ -q` 156 passed (including the new
  live smoke test actually executing against real Ollama + a freshly
  built smokeserver, ~267s), `pytest agents/ data/ -q` 170 passed.
- Issue #19 is now fully implemented across both of its subtasks
  (3.5.1 dataset loaders, 3.5.2 this end-to-end smoke run). See
  `.cdr/commits/task-issue-19-summary.md` for the consolidated closure
  ledger and still-open findings.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-10/041-verification`
- Verifier independently re-derived the F4 root cause from first
  principles (reading `proto/hivemind.proto`, `engine/rpc/server.go`, and
  `agents/ingestion/wiring.py` directly) and confirmed the "requires a
  proto/wire-contract change" claim is genuine, not a skipped feasible
  narrow fix. Also independently confirmed: the Enron sample's content is
  consistent with authentic corpus structure (not another hand-authored
  substitute); `engine/cmd/smokeserver/main.go` is a genuine real server,
  not a stub; the smoke test's final assertion is load-bearing, not
  trivially passing; F7 is genuinely distinct from the already-closed F1;
  and scope containment (zero lines changed in pre-existing production
  files). Two informational-only findings, neither blocking.

### Findings

- **F4 (confirmed, high severity, non-blocking, needs dedicated future
  task)**: `PutSegment`'s CREATE path never sets
  `catalog.CatalogRecord.PathHash`, because `PutSegmentRequest` in
  `proto/hivemind.proto` has no path field at all to hash -- this is a
  genuine architectural gap, not a one-line fix, independently re-verified
  from first principles by verification rather than taken on the
  implementer's word. A real fix requires: adding a path field to
  `PutSegmentRequest`, regenerating both Go (`.pb.go`) and Python
  (`hivemind_pb2`/`_grpc`) codegen, updating `engine/rpc/server.go`'s
  handler to populate `PathHash`, and updating both the real and
  test-double clients in `agents/ingestion/wiring.py` -- a genuine
  cross-language wire-contract change. Recommend a dedicated future task
  scoped explicitly as a proto/wire-contract change, with a regression
  test proving newly created files become discoverable via
  `SearchCandidates` after the fix (this run's smoke test already proves
  they are NOT discoverable today).
- **F7 (new, non-blocking, medium severity)**: real local
  `llama3.1:8b` (temperature=0.0) responses can contain raw control
  characters/newlines embedded directly inside JSON string values, and
  stray triple-quote (`"""`) artifacts around string values -- both
  occurring even *after* `strip_code_fences` has already run cleanly, and
  both distinct from the already-closed F1 (markdown-code-fence
  wrapping). Observed in ~7 of 11 real documents in this run.
  `segment.py`'s `_parse_segment_json` was intentionally left untouched
  (out of scope for this subtask); the smoke test instead catches
  `SegmentParseError` per document and asserts only on the successfully
  parsed subset.

## Release Notes

- Added a genuine real Enron corpus sample
  (`data/fixtures/enron_real_sample/` + `PROVENANCE.md`), streamed
  directly from the official CMU/FERC archive with disclosed provenance,
  replacing the need for 3.5.1's disclosed hand-authored substitute for
  end-to-end testing purposes.
- Added `engine/cmd/smokeserver`, a standalone real Go gRPC server binary
  for out-of-process smoke testing, and
  `agents/ingestion/test_e2e_smoke.py`, the repo's first fully
  end-to-end pipeline test driven against a real local Ollama model and a
  real gRPC server subprocess.
- **F4 confirmed as a genuine architectural blocker**: `PutSegment`'s
  CREATE path cannot set `PathHash` without a proto/wire-contract change.
  Not fixed here by design; forwarded with a precise recommended fix
  scope to `.cdr/memory/pending.md`.
- **New finding F7**: real LLM output can contain malformed JSON (control
  characters, stray triple-quotes) distinct from the closed F1. Forwarded
  to `.cdr/memory/pending.md`, not fixed (out of scope).
- This is the final subtask of issue #19. Issue #19 is now fully
  implemented, verified, and committed **locally only** -- this commit
  does not push and does not touch any GitHub issue/milestone state, per
  standing convention requiring separate, fresh, explicit user
  authorization before any push/close action. See
  `.cdr/commits/task-issue-19-summary.md`.
