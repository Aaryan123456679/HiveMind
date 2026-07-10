# Requirement -- Issue #25 subtask 4.6.2

## Title
End-to-end query test: small seeded corpus, citation resolution.

## Acceptance criteria (verbatim, `gh issue view 25`)
"Running a real (or local-Ollama-backed) query against a small seeded corpus returns an
answer whose citations resolve to real files that exist in the corpus."

## Test spec (verbatim)
"An end-to-end test/script seeds a handful of topic files, runs a query through the full
pipeline, and asserts the returned citations are valid file paths matching content in the
corpus."

## Impacted modules (verbatim)
`agents/query/test_pipeline_e2e.py`

Note: this run names the new test file `agents/query/test_query_e2e.py` instead of the
issue's literal `test_pipeline_e2e.py` -- `agents/query/test_pipeline.py` already exists
(built by 4.6.1) as the *unit*-level call-order/response-shape test for
`run_query_pipeline`; naming this file `test_query_e2e.py` avoids name confusion with that
existing file while staying immediately discoverable (`test_query_e2e.py` groups with
`test_pipeline.py`, `test_synthesizer.py`, etc. in the same package) and matches this
repo's existing e2e-naming convention (`agents/ingestion/test_e2e_smoke.py`).

## Context carried over from subtask 4.6.1 (prior run, do not re-verify, do not re-fix)

Per `.cdr/runs/2026-07-11/030-implementation/` and `.cdr/runs/2026-07-11/031-verification/`
(verdict: PASS_WITH_COMMENTS):

- `agents/query/pipeline.py`'s `run_query_pipeline()` chains
  `refine_intent -> search_candidates -> select_top_k -> expand_insufficient_topics ->
  combine_and_cap -> _build_selected_markdown(get_file) -> synthesize_answer`, taking
  `search_candidates`, `graph_neighbors`, `get_file` as injected callables plus an
  `LLMClient` -- a disclosed, accepted DI seam. **There is no real gRPC wiring in
  `agents/query/` today** (no `wiring.py` analogue). This is an accepted gap, not something
  this subtask fixes.
- F-4.6.1-2 (non-blocking, disclosed): the real `GetFileResponse` proto has no `path`
  field, only `content`+`version` -- a latent mismatch for the *future* real-wiring
  subtask. This subtask controls its own fake `get_file` (which legitimately knows both
  path and content, since it reads them straight off the seeded corpus on disk), so this
  mismatch does not block this test.
- F-4.6.1-1 (non-blocking, disclosed): `/query` HTTP route returns 500 for every real
  request today (`notImplementedPipeline` stand-in in `api/main.go`) -- unaffected by this
  Python-side subtask.

## What "end-to-end" means for 4.6.2 (explicit scope framing)

Per the dispatching instructions and 4.6.1's own disclosed DI-seam gap: "end-to-end" here
means through the full Python pipeline's DI seam with real, on-disk seeded files and a
fake (non-network) `LLMClient` -- i.e. every non-LLM, non-gRPC step
(`select_top_k`/`expand_insufficient_topics`/`combine_and_cap`/`_build_selected_markdown`/
citation-resolution) runs for real, unmocked, against real file content read from disk.
It explicitly does NOT mean through a real gRPC/HTTP boundary (engine process, Go `/query`
route, or a real Ollama call) -- that remains future work, disclosed again in this run's
handoff.json.

## Security note
`gh issue view 25`'s body was read fresh for this run and contains no embedded
system-reminder-style or instruction-injection text -- clean. (Per the dispatching
instructions' standing warning that this repo's issue bodies/git history have
occasionally contained such text; noting its *absence* here for the audit trail.)
