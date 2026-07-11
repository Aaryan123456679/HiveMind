# Architecture discovery -- subtask 5.3.4

Read in order: `docs/HLD.md` (#7 known risks), `docs/LLD/eval.md`, then `.cdr/index/*.jsonl`
(no matching entries for run_benchmark/chart yet -- this is new ground), then the actual
already-merged 5.1.x/5.2.x/5.3.x/5.4.1 source files in `agents/eval/`.

## Key findings

1. **"All three arms" resolved from `pipeline.py`'s own docstring, not guessed.**
   `agents/eval/pipeline.py`'s module docstring literally enumerates "one of the three per-arm
   wrapper functions (`run_hivemind_arm`, `run_vector_rag_arm`, `run_graphrag_lite_arm`)" as the
   complete wrapper set. There is no `run_vector_rag_rerank_arm` in `pipeline.py` --
   `vector_rag_rerank.py` (5.2.2) exists as an independent baseline module but was never wired
   into `pipeline.py`'s shared-final-answer enforcement mechanism. `docs/LLD/eval.md`'s own
   "Retrieval arms" section also lists exactly three arms (HiveMind, classic vector RAG,
   simplified GraphRAG-style). Conclusion: "all three arms" = `{hivemind, vector_rag,
   graphrag_lite}`, i.e. exactly `pipeline.py`'s three wrappers. `vector_rag_rerank` is
   deliberately NOT wired into `run_benchmark.py` in this pass (it isn't one of `pipeline.py`'s
   arms and wiring it in would require inventing a 4th `pipeline.py` wrapper, out of this
   subtask's impacted-module list of `run_benchmark.py` + `chart.py` only).

2. **No existing corpus-checkpoint-by-percentage utility anywhere in the repo.**
   Checked `agents/eval/` (datasets.py only supports `limit=` truncation of a dataset loader
   iterator, no percentage concept) and `agents/ingestion/` (dispatch/normalize/segment/shortlist
   -- no checkpoint concept at all). `eval/traversal_precision.py`'s `CorpusGrowthCheckpoint` is
   the closest existing shape (`label` + `(doc_id, text)` list) but is explicitly documented as
   "Wiring real 20/50/100-ingested corpus snapshots into this shape is deferred ... subtask
   5.3.4's explicitly-gated 'real benchmark execution' scope" -- i.e. that module expects THIS
   subtask to define the slicing mechanism. `run_benchmark.py` therefore defines a minimal,
   clearly-scoped `checkpoint_corpus()` (percentage-prefix slice of an ordered `(doc_id, text)`
   list) and a `CorpusCheckpoint` dataclass; `traversal_precision.CorpusGrowthCheckpoint` is
   reused (not duplicated) wherever `compare_precision_across_checkpoints` is actually called.

3. **`pipeline.py`'s `run_hivemind_arm` takes pre-retrieved `retrieved_doc_ids` -- scope
   boundary carried forward.** Real HiveMind retrieval is the gRPC-backed
   `query.pipeline.run_query_pipeline()`, explicitly out of `pipeline.py`'s own scope (issue
   #25/#56). `run_benchmark.py` mirrors this: it accepts an injected
   `hivemind_retriever: Callable[[QueryLabel, Mapping[str,str]], list[str]]` standing in for the
   real engine call, so the harness is provably wireable to the real pipeline later without any
   change to `run_benchmark.py`'s own orchestration logic.

4. **Scoring reuses `eval.metrics.score_arm` (5.3.1) uniformly across all three arms** --
   `vector_rag.retrieve_documents` / `graphrag_lite.retrieve_documents` / the injected
   `hivemind_retriever` all return the same ranked `list[str]` shape `score_arm` expects, so no
   arm-specific scoring branch is needed.

5. **Cost/latency wiring (5.3.3 + interceptor issue #59).** Final-answer generation in this
   offline/Ollama-only pass never goes through a paid provider, so `run_benchmark.py` records a
   `StageRecord(provider="ollama", cost_usd=None)` per retrieval+final-answer call (resolved to
   `$0.0` by `cost_latency.resolve_cost_usd`'s existing free-provider rule) rather than routing
   through `LLMInterceptor` for that step -- `LLMInterceptor` is reserved, per this pass's
   binding instruction, exclusively for the *judge* scoring path (`llm_judge.score_arm_answers`,
   which already calls `LLMInterceptor.call()` internally), the only place a real paid call
   could ever occur. Judge scoring is wired as an **optional** path (`judge_config=None` by
   default) -- disabled in this implementation's own tests (stubbed judge client + interceptor
   used only to prove the wiring compiles/runs end-to-end offline, per instruction (d)).

6. **`compare_precision_across_checkpoints` (5.4.1) multi-checkpoint test gap.** Per the
   launching agent's brief, 5.4.1's own verification run
   (`.cdr/runs/2026-07-12/005-verification`) flagged that the shipped
   `test_traversal_precision_check.py` never calls `compare_precision_across_checkpoints()` with
   more than one checkpoint in a single call. This subtask's own test suite
   (`test_run_benchmark.py`) adds a direct multi-checkpoint (3-item) call to
   `compare_precision_across_checkpoints()` and asserts per-checkpoint independence (each
   checkpoint's own `TraversalPrecisionComparison.checkpoint_label` and scores line up
   positionally with the input list, and one checkpoint's corpus does not leak into another's
   `EntityGraph`), closing the gap directly rather than trusting the un-tested multi-checkpoint
   scale.

7. **Charting approach.** `agents/pyproject.toml` has no matplotlib/plotting dependency (only
   fastapi/uvicorn/grpcio/pydantic/httpx/pymupdf). Per this subtask's own instruction ("prefer
   zero-new-dependency if a reasonable option exists"), `chart.py` renders the degradation chart
   as a stdlib-only text/data table (one row per checkpoint, one column-group per arm, showing
   recall/precision), not a PNG/matplotlib figure. No new dependency added.

## No index/handoff hits
`.cdr/index/*.jsonl` has no entries yet for `run_benchmark`/`chart.py` (new files). Prior
handoffs consulted: 5.2.4 (`pipeline.py`), 5.3.1 (`metrics.py`), 5.3.2 (`llm_judge.py`), 5.3.3
(`cost_latency.py`), issue #59 (`llm/interceptor.py`), 5.4.1 (`traversal_precision.py`) -- all
read directly from source per the "read the actual wrapper set, don't guess" instruction, since
no LLD/index entry resolves the 3-vs-4-arm ambiguity on its own.
