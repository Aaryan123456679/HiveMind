# Plan — 4.5.19.2

1. `docs/LLD/ingestion-agent.md`:
   - Front-matter: bump `last_synced_commit` to current HEAD SHA.
   - "Status" line: replace "scaffold only" with an accurate implemented-status line
     naming the real modules.
   - "Segmentation agent": replace the hardcoded "local Ollama model" claim with a
     description of `llm.factory.create_llm_client()`'s config-driven provider selection
     (ollama/openrouter/gemini), noting local Ollama as the still-recommended default for
     ingestion call-volume/cost reasons.
   - "What the Go engine does with each segment": rewrite to describe
     `agents/ingestion/wiring.py`'s `execute_segment()` as the real orchestrator driving
     `PutSegment` (with real `path`), `LookupEntity`/`PutEntity` (entity index),
     `PutEdge` (`ENTITY_COOCCUR`/`LLM_ASSERTED`), with fail-fast/best-effort semantics.
   - "Interactions with other modules": add `PutEdge`/`PutEntity`/`LookupEntity` alongside
     `PutSegment`.
   - Leave "Purpose", "Per-doc-type normalization" bullets, "ProposeSplit", "Known risks",
     "Cross-references" untouched (already accurate).

2. `docs/LLD/query-agent.md`:
   - Front-matter: bump `last_synced_commit` to current HEAD SHA.
   - "Status" line: replace "scaffold only" with accurate status.
   - New section after "Pipeline order": "Pipeline wiring & real gRPC surface" describing
     `pipeline.py` (`run_query_pipeline`), `wiring.py` (outbound
     `GrpcSearchCandidatesClient`/`GrpcGraphNeighborsClient`/`GrpcGetFileClient`), and
     `server.py` (inbound `RunQuery` gRPC server backing `api/routes/query.go`'s `/query`
     route), per issue #56.
   - "Interactions with other modules": add the real gRPC wiring detail.
   - Do NOT touch "Known risks" (owned by 4.5.19.4).

3. Update `.cdr/index/file.jsonl` `last_change_run` for both doc rows to this run id.
4. Append one `.cdr/index/feature.jsonl` record documenting the doc-sync pass.
5. Produce `drift-report.json` showing before/after drift per file, ending at zero.
6. Self-consistency check (front-matter parses, no dangling markdown links, sibling
   section byte-identical).
7. One local commit (no push).
8. `handoff.json` with pointers only.
