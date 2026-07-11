# Requirement — Subtask 4.5.19.2 (issue #58)

**Priority**: LOW
**Type**: LLD-sync (doc-only, `/cdr:doc --mode sync` workflow embedded in `/cdr:implement`)

## Statement

`docs/LLD/ingestion-agent.md` and `docs/LLD/query-agent.md` both carry
`last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317` (the 2026-07-03 bootstrap
commit) in their front-matter, but:

- `agents/ingestion/*` has received 26+ commits since that bootstrap (dispatch.py,
  rawdoc.py, shortlist.py, segment.py, wiring.py, propose_split.py, plus the
  `PutEdge`/`PutEntity`/`LookupEntity` engine RPC additions) — the doc still says
  "Status: scaffold only".
- `agents/query/*` has received 18+ commits (intent_refiner.py, topic_selector.py,
  synthesizer.py, pipeline.py, wiring.py, server.py) — the doc still says "Status:
  scaffold only" and omits the full pipeline-wiring / real-gRPC-client / RunQuery-server
  layer entirely.

Sibling subtask 4.5.19.4 (commit `68c3c5c`) already landed a comment-only wording polish
to `query-agent.md`'s "Known risks" `SearchCandidates` term-cap section. That section must
be preserved byte-for-byte; this subtask must not reintroduce drift there or conflict with
it.

## Acceptance criteria

1. Regenerate **only the drifted sections** of both docs — preserve accurate existing
   content unchanged.
2. Ground the regenerated sections in the actual current
   `agents/ingestion/*.py` / `agents/query/*.py` source (not just commit messages).
3. Stamp `last_synced_commit` in both docs' front-matter with current HEAD's commit SHA.
4. Update `.cdr/index/{feature,file}.jsonl` to reflect the sync.
5. Produce a drift report showing zero remaining drift for both files after sync.

## Test spec

Drift report at `.cdr/runs/2026-07-11/9500-implementation/drift-report.json` shows zero
remaining drift for both files after sync.

## Impacted modules

- `docs/LLD/ingestion-agent.md`
- `docs/LLD/query-agent.md`
- `.cdr/index/feature.jsonl`
- `.cdr/index/file.jsonl`

## Constraints

- Doc-sync only; no production code changes.
- No self-verification (invariant I4) — verification is delegated to `/cdr:verify`.
- One local commit only, no push.
