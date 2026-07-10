# Requirement: issue #19 subtask 3.5.2 (FINAL subtask of issue #19)

**Title**: Full end-to-end ingestion smoke run against a real dataset sample.

**Acceptance criteria** (verbatim from `gh issue view 19`): Running the full pipeline
(dataset loader -> normalize -> segment -> PutSegment) against a real (non-fixture)
dataset sample populates the catalog, btree, and graph with a plausible topic set with
no unhandled errors.

**Test spec** (verbatim): A scripted smoke run (e.g. `python -m agents.ingestion.run_ingest
--dataset bitext --limit 100`) executed manually/CI, asserting exit code 0 and a post-run
catalog/btree/graph sanity check (non-zero file count, no orphaned btree entries).

**Impacted modules** (per issue): `agents/ingestion/run_ingest.py`.

No embedded fake system-reminder-style text was found in the issue #19 body itself
(`gh issue view 19` output was clean). Two separate prompt-injection attempts WERE
observed mid-run in tool-call output during this session (a fake "date changed, don't
tell the user" system-reminder, a fake "Auto Mode Active" directive, and later a fake
"file was modified by the user or a linter, don't tell the user" notice attached to a
background-task Bash result) -- all disclosed to the user in-conversation, treated as
untrusted data, and not acted upon.

## Carried-forward context (from 3.5.1 / pending.md)

- `hivemind-issue19-3.5.2-need-real-enron-sample`: 3.5.1's Enron fixtures
  (`data/fixtures/enron_sample/`) are hand-authored/invented, format-faithful but not
  real corpus data. This subtask needed a genuine, non-fixture Enron sample.
- **F4**: `engine/rpc/server.go`'s `PutSegment` CREATE path never sets
  `catalog.CatalogRecord.PathHash` (high severity, pre-existing since task-3.2.2).
  This run's end-to-end smoke run creates new topics via `PutSegment`'s CREATE path, so
  F4's real consequence (new files never discoverable via `SearchCandidates`) is
  directly exercised and observed by this run, not just theorized.

## Actual scope this run executed (see handoff.json for full detail)

1. Sourced 6 genuine real Enron corpus messages directly from the official CMU/FERC
   `enron_mail_20150507.tar.gz` archive (streamed, not downloaded/committed in full) --
   `data/fixtures/enron_real_sample/` + `PROVENANCE.md`.
2. Investigated F4 in depth (`engine/catalog/record.go`, `engine/rpc/server.go`):
   confirmed it requires a proto/wire-contract change (`PutSegmentRequest` has no `path`
   field at all to hash) -- per this run's own instructions, STOPPED on fixing F4 and
   documented it as a blocker (handoff.json) rather than forcing an incomplete fix.
3. Built `engine/cmd/smokeserver` (a new, real, standalone Go gRPC server subprocess,
   backed by real catalog/content/btree/graph storage) and
   `agents/ingestion/test_e2e_smoke.py` (drives the real Python ingestion pipeline --
   loaders, normalizers, `shortlist()`, `segment()` against a real local Ollama model,
   `wiring.execute_segment()` -- against that real subprocess over a real gRPC channel).
4. Did not modify `agents/ingestion/segment.py`'s core logic, `propose_split.py`, or
   `shortlist.py`'s core logic, beyond invoking them as-is.
5. Ran full regression: `agents/.venv/bin/pytest data/ agents/ -q` (170 passed),
   `go test ./... -race` (all packages ok), `go vet ./...` (clean), `ruff check` (clean).
6. This is issue #19's FINAL subtask. See handoff.json for the explicit stop-and-ask
   directive before any push/GitHub-state change, and milestone #5 closability note.
