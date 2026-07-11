# Requirement -- Subtask 5.2.3

Source: GitHub issue #27 (milestone #7 / Phase 5, "Benchmark harness -- retrieval quality"),
subtask 5.2.3, "Simplified GraphRAG-style baseline: entity-graph retrieval".

## Acceptance criteria (verbatim from issue)

An entity-graph-based retrieval baseline (extract entities, build/query an entity graph,
retrieve associated chunks/summaries) is implemented as the second baseline arm.

## Test spec (verbatim from issue)

`pytest agents/eval/test_graphrag_baseline.py`: run against a fixture corpus + fixture queries,
assert plausible entity-graph-driven retrieval results.

## Impacted modules (per issue)

- `agents/eval/baselines/graphrag_lite.py` (new)
- `agents/eval/test_graphrag_baseline.py` (new)

## Standing constraints (carried over from prior 5.2.x subtasks + this run's explicit
instructions)

1. Ollama-only for any LLM-backed work; no OpenRouter/Gemini; no `.env`; use the existing
   hardcoded-local-Ollama client pattern (`agents/llm/ollama_client.py`'s `OllamaClient`,
   instantiated directly the same way 5.2.2's `vector_rag_rerank.py`/its test file do -- not
   via `agents.llm.factory.create_llm_client`'s env-var-driven path, though that factory's
   `provider="ollama"` explicit-arg path would be an equally compliant literal if used).
2. No new heavyweight ML/graph dependency (no `networkx`, no torch/sentence-transformers/etc.)
   unless truly unavoidable -- prefer a stdlib-only graph representation (dict-of-sets
   adjacency), matching 5.2.1/5.2.2's "no new embedding/rerank library" precedent.
3. Benchmark fairness: this must be a genuine second arm (real entity extraction + real graph
   traversal/retrieval), not a strawman -- matching 5.2.1's "real, non-strawman chunk
   size/overlap tuning" standard and issue #27's own system-wide risk callout
   (`docs/HLD.md` #7, `docs/LLD/eval.md`'s "Benchmark fairness" + "Graph traversal context
   blow-up" known risks).
4. Reuse existing infrastructure/schemas where sensible: match `vector_rag.py`'s
   `retrieve_documents(...) -> list[str]` (ranked document ids) output shape so a future
   metrics pipeline (issue #28, not yet built) can treat all baseline arms uniformly. Study
   `agents/eval/datasets.py` (5.1.1), `agents/eval/ground_truth.py` (5.1.3), and
   `agents/query/topic_selector.py`'s graph-neighbor-expansion design (4.4.2/4.4.3, Go-engine
   delegated -- not directly reusable in-process here, but informs the local hop-expansion
   design) before designing.
5. Standard CDR workflow: requirement -> architecture-discovery -> impact-analysis -> plan ->
   validation-matrix -> implement -> self-consistency -> ONE local commit -> handoff. No
   self-verification (leave to `/cdr:verify`).
