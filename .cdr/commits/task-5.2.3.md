# task-5.2.3: Entity-graph (GraphRAG-lite) retrieval baseline (issue #27)

## Summary

**Problem:** Per docs/LLD/eval.md, the milestone #7 benchmark suite compares
three retrieval arms so that only the retrieval step varies: HiveMind itself,
the classic vector-RAG baseline (5.2.1/5.2.2), and a "Simplified
GraphRAG-style" entity-graph baseline — the third arm, not yet implemented.
Per docs/HLD.md #7's stated "Benchmark fairness" and "Graph traversal context
blow-up" risks, this arm had to be a genuine second baseline (real entity
extraction plus real graph traversal), not a strawman, and any multi-hop
expansion could not be allowed to silently dilute precision.

**Solution:** Added `agents/eval/baselines/graphrag_lite.py`: entities are
extracted from documents and queries via a prompted local Ollama completion
call (`llama3.1:8b`, through the existing `LLMClient` interface — no new
client abstraction, consistent with 5.2.2's precedent), with a non-primary
capitalized-run heuristic fallback used only when the LLM output is
unparseable. A stdlib-only `EntityGraph` (dict-of-sets adjacency: entity to
docs, entity to co-occurring entities, doc to entities) is built by linking
every pair of entities extracted from the same document — no new dependency
(no networkx), consistent with 5.2.1/5.2.2's "no new heavyweight dependency"
precedent. `retrieve_documents()` resolves a query's own extracted entities
against the graph (exact match plus a disclosed substring-containment
fallback for paraphrase tolerance), performs a capped-hop BFS walk outward
with strictly decaying per-hop weight (`HOP_DECAY ** hop`) as the disclosed
mitigation for the "graph traversal context blow-up" risk, and returns ranked
document ids matching `vector_rag.retrieve_documents`'s exact `list[str]`
output shape for future cross-arm metrics consistency (issue #28, not yet
built). Added `agents/eval/test_graphrag_baseline.py` covering pure-unit
JSON/heuristic parsing via a stub `LLMClient`, hop-expansion behavior, and a
live-local-Ollama tier exercising real entity-graph-driven retrieval on the
fixture corpus/queries.

## Features

- `agents/eval/baselines/graphrag_lite.py`: third (entity-graph) retrieval
  arm for the milestone #7 benchmark suite — LLM-based entity extraction,
  stdlib co-occurrence graph, capped-hop decayed BFS traversal for query
  resolution, output shape aligned with the existing vector-RAG baseline.
- `agents/eval/test_graphrag_baseline.py`: unit tests (stubbed LLM,
  JSON/heuristic parsing, hop-expansion/decay behavior) plus a live-local
  Ollama tier (skips if unreachable) proving retrieval is genuinely
  graph-traversal-driven and can recover a document with zero lexical
  overlap with the query.

## Impact

- 2 files added (`agents/eval/baselines/graphrag_lite.py`,
  `agents/eval/test_graphrag_baseline.py`); no existing file modified,
  including `vector_rag.py` (not touched by this subtask).
- No new dependency: `git diff` against `agents/pyproject.toml` is empty;
  `networkx` appears only in a docstring explaining why it was deliberately
  not added. 100% local/Ollama-only — no OpenRouter/Gemini/API-key usage
  anywhere in the new module or its tests.
- Full `agents/eval/` suite: 83/83 passed; new test file: 21/21 passed
  (including live-Ollama cases), reproduced across 2 independent re-runs by
  the verifier with no flakiness.
- Completes the third and final retrieval arm required by docs/LLD/eval.md's
  three-arm benchmark design (HiveMind vs. vector-RAG vs. GraphRAG-lite);
  unblocks milestone #7's cross-arm benchmark comparison work (issue #28+).
- One real, non-blocking gap identified during verification (see below):
  the paraphrase-tolerance substring-containment fallback in
  `_match_query_entities` has no minimum-length/stopword guard, unlike the
  sibling `_heuristic_entities` fallback which does. This is a latent
  benchmark-fairness risk (over-permissive matching) at real-corpus scale,
  not a defect in the current fixture-scale behavior.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-11/10016-verification`
- Commit: `2697d0746583dea2d5a0892c610bd3e0c6d42c41`
- Zero blocking findings. Verifier independently confirmed: no new
  dependency added (`agents/pyproject.toml` diff empty; `networkx` only in
  prose); Ollama-only guarantee holds (no OpenRouter/Gemini/API-key
  references); output shape matches `vector_rag.py` exactly; full `eval/`
  suite (83/83) and new test file (21/21, including live-Ollama cases) pass
  on 2 independent re-runs with no flakiness; graph traversal is genuinely
  exercised (independently reproduced recovering a document with zero
  lexical overlap with the query, ruling out a hidden vector-similarity
  shortcut).
- One medium-severity non-blocking finding logged in
  `.cdr/index/regression.jsonl`: the substring-containment paraphrase-
  tolerance fallback in `_match_query_entities` has no minimum-length or
  stopword guard and can false-positive on very short query entities (e.g.
  a single-character entity like `"a"` matching both `"apple"` and
  `"banana"`), a latent benchmark-fairness risk at real-corpus scale. No
  existing test exercises this failure direction. Recommended fix: add a
  minimum canonical-entity-length guard (mirroring the existing
  `_MIN_HEURISTIC_ENTITY_WORDS` intent) or require meaningful word-token
  overlap instead of raw character containment, plus an adversarial test,
  before this baseline is wired to a real corpus (tracked under issue #28+).

## Release Notes

- Added the GraphRAG-lite (entity-graph) retrieval baseline
  (`agents/eval/baselines/graphrag_lite.py`), completing the third and final
  retrieval arm of the milestone #7 benchmark suite alongside HiveMind and
  the classic vector-RAG baseline (5.2.1/5.2.2). Local-only (Ollama
  `llama3.1:8b`), no new dependency, no new client abstraction.
- Non-blocking follow-up tracked in `.cdr/index/regression.jsonl`
  (`hivemind-issue27-5.2.3-...` finding for run 10016-verification): harden
  the paraphrase-tolerance entity-matching fallback with a minimum-length or
  word-token-overlap guard before benchmarking against a real corpus.
