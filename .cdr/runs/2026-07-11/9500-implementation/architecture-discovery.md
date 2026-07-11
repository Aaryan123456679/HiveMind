# Architecture Discovery — 4.5.19.2

## Token order followed

1. `.cdr/index/file.jsonl` — confirmed both docs' `last_change_run` still points at
   `2026-07-03-001-documentation` (the bootstrap doc-generation run).
2. `.cdr/index/feature.jsonl` — found 8 ingestion-module features (subtasks 3.4.2–3.5.2)
   and 12 query-module features (subtasks 4.3.1–4.6.2) referencing `docs/LLD/ingestion-agent.md`
   / `docs/LLD/query-agent.md` as their doc target, none of which were folded back into the
   docs themselves.
3. `git log 699105b..HEAD -- agents/ingestion/` (26 commits) and
   `git log 699105b..HEAD -- agents/query/` (18 commits) — corroborated the drift-scope
   claim in the task brief.
4. Targeted LLD read: full current text of both `docs/LLD/ingestion-agent.md` and
   `docs/LLD/query-agent.md`.
5. Sibling-subtask check: `git show 68c3c5c -- docs/LLD/query-agent.md` — confirmed the
   diff touches only the "Known risks" `SearchCandidates` term-cap prose (comment/wording
   polish, no structural change). That section is left untouched by this sync.
6. Source (grounding read, permitted for sync targets): `agents/ingestion/rawdoc.py`,
   `dispatch.py`, `segment.py`, `shortlist.py`, `propose_split.py`, `wiring.py`;
   `agents/query/intent_refiner.py`, `topic_selector.py`, `pipeline.py`, `wiring.py`,
   `server.py`; `agents/llm/factory.py`.

## Key drift findings

### `docs/LLD/ingestion-agent.md`

- "Status: scaffold only" is false — all 26 subtask commits since bootstrap fully
  implemented `dispatch.py`/`rawdoc.py`/`shortlist.py`/`segment.py`/`propose_split.py`/
  `wiring.py` plus a real end-to-end smoke test (issue #19).
- "Segmentation agent ... using a local Ollama model" is stale: `segment.py` now resolves
  its `LLMClient` via `agents.llm.factory.create_llm_client()`, a config-driven factory
  supporting `ollama` / `openrouter` / `gemini` (`LLM_PROVIDER` env var), not a hardcoded
  local-only Ollama call. Local Ollama remains the recommended default for cost reasons but
  is no longer the only option.
- "What the Go engine does with each segment" undersold: `agents/ingestion/wiring.py`'s
  `execute_segment()` is the real orchestrator, calling `PutSegment` (now with a real
  `path` field, closing issue #43) then `LookupEntity`/`PutEntity`/`PutEdge` (added by
  `task-3.4.4-engine-edge-rpc`, a user-authorized scope addition, to back
  `ENTITY_COOCCUR`/`LLM_ASSERTED` edges) with fail-fast-then-best-effort error semantics.
  None of the `PutEdge`/`PutEntity`/`LookupEntity` RPCs or the real `path`-carrying
  `PutSegment` were reflected in the doc.
- `shortlist.py`'s real `GrpcSearchCandidatesClient` (wrapping `HiveMindStub.SearchCandidates`)
  existed but wasn't named.

### `docs/LLD/query-agent.md`

- "Status: scaffold only" is false — `intent_refiner.py`, `topic_selector.py`,
  `synthesizer.py` are fully implemented, and two entire modules are missing from the doc
  altogether: `pipeline.py` (`run_query_pipeline()`, the single entry point chaining all
  three agents, wired to `api/routes/query.go`'s `/query` route) and `wiring.py` +
  `server.py` (real outbound `GrpcSearchCandidatesClient`/`GrpcGraphNeighborsClient`/
  `GrpcGetFileClient` plus a real inbound `RunQuery` gRPC server so the Go `api/` gateway
  can invoke the Python pipeline over the network, per issue #56).
- "Known risks" `SearchCandidates` term-cap section: verified unchanged from `68c3c5c`,
  left untouched.

## Non-goals for this pass

- No changes to `agents/*` source.
- No changes to the "Known risks" section content already polished by 4.5.19.4.
- No renumbering of existing accurate paragraphs.
