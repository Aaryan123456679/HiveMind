# Architecture Discovery -- Subtask 5.2.3

Docs read first (per token order): `docs/HLD.md` #1, #7; `docs/LLD/eval.md`; `.cdr/index/feature.jsonl`
and `.cdr/index/task.jsonl` entries for 5.2.1/5.2.2; prior handoffs
(`.cdr/runs/2026-07-11/10011-cdr-commit`, `10012-implementation`, `10013-verification`,
`10014-cdr-commit`). Only then read touched/related source files.

## 1. Where this fits

`docs/LLD/eval.md` names three retrieval arms compared in `agents/eval/`:
1. HiveMind (full pipeline, not this subtask's concern)
2. Classic vector RAG (5.2.1 `vector_rag.py`, optionally reranked by 5.2.2 `vector_rag_rerank.py`)
3. **Simplified GraphRAG-style -- entity-graph retrieval (this subtask, 5.2.3)**

`docs/HLD.md` #7 "System-wide known risks" calls out two risks directly relevant here:
- "Benchmark fairness -- the vector-RAG baseline must be well-tuned... not a strawman" (extends
  to this arm too, per this run's explicit instructions: genuine entity extraction + genuine
  graph traversal, not a weakened stand-in).
- "Graph traversal context blow-up -- bounded by a hard file-count cap... the benchmark must
  measure whether traversal ever hurts precision, not just recall." This is HiveMind's own
  graph-expansion risk (`agents/query/topic_selector.py`'s `GraphNeighbors` hop expansion,
  4.4.2/4.4.3), but `docs/LLD/eval.md`'s own "Known risks" section repeats it for `agents/eval/`
  generally, and this baseline arm is itself a graph-traversal retrieval method, so the same
  blow-up concern applies to it directly: multi-hop entity-graph expansion can pull in
  precision-diluting off-topic documents just as HiveMind's own file-graph expansion can.
  Mitigation chosen here: hop-decay weighting (see section 3) plus a small `max_hops` default,
  disclosed below -- full corpus-scale "does expansion actually hurt precision" measurement is
  explicitly deferred to the future metrics pipeline (issue #28), matching this arm's own
  fixture-only scope (see section 5) and mirroring 5.2.1/5.2.2's identical scope-boundary
  disclosure.

## 2. Prior-subtask precedent studied (per this run's explicit instruction to reuse infra)

- `agents/eval/baselines/vector_rag.py` (5.2.1): output shape is `retrieve_documents(...) ->
  list[str]` (ranked document ids, chunk-hits aggregated to document level via max-score-per-doc).
  This subtask's `retrieve_documents` matches that exact shape so a future metrics pipeline can
  treat all arms uniformly (this run's explicit constraint #4). Also reuses `recall_at_k`
  unmodified (imported, not reimplemented) for the live test's aggregate check.
- `agents/eval/baselines/vector_rag_rerank.py` (5.2.2): established the precedent that an
  LLM-backed second-stage step should use `agents.llm.client.LLMClient`'s existing
  `complete(prompt) -> str` shape directly (not a new bespoke client), because prompted
  text-in/text-out completion already matches that interface -- unlike 5.2.1's embeddings, which
  needed a bespoke `OllamaEmbeddingClient` because the embedding call shape doesn't fit
  `LLMClient` at all. Entity extraction here is exactly the same "prompted text-in/text-out"
  shape, so this subtask follows 5.2.2's precedent: prompt the existing local
  `agents.llm.ollama_client.OllamaClient` (`llama3.1:8b`) via `LLMClient.complete`, no new client
  class. `precision_at_k` is reused (imported) from `vector_rag_rerank.py` rather than
  reimplemented, for the same "shared metric functions across arms" consistency goal.
  `vector_rag_rerank.py`'s and its test file's own instantiation pattern
  (`OllamaClient(model=_LLM_MODEL)`, direct construction, no env var, no `.env`, no
  `agents.llm.factory`) is followed identically here -- this is the "hardcoded-local-Ollama
  client path established in prior subtasks" this run's instructions refer to; neither 5.2.1 nor
  5.2.2 actually route through `agents.llm.factory.create_llm_client(provider="ollama")` (that
  factory exists per issue #20/4.1.3 for `agents/ingestion/`+`agents/query/` production call
  sites, config-driven; `agents/eval/`'s baselines have never used it), so this subtask matches
  the *actual* established eval-baseline precedent (direct `OllamaClient(...)` construction)
  rather than introducing a new pattern within `agents/eval/`.
- `json_fences.py` (top-level shared module, subtask 3.4.6/4.5.17.2): `strip_code_fences` +
  `sanitize_control_chars_and_triple_quotes` are the repo's established, already-hardened way to
  clean a local Ollama model's raw completion string before `json.loads`, used by
  `ingestion/segment.py` for structured JSON extraction from `llama3.1:8b`. This subtask reuses
  both helpers unmodified for parsing the LLM's entity-list JSON response, rather than
  reimplementing fence-stripping/control-char-sanitizing logic a third time.
- `agents/query/topic_selector.py` (4.4.2/4.4.3): established the "hop-based graph expansion,
  capped, weighted/scored, deduplicated" shape for HiveMind's own file-graph traversal
  (`GraphNeighborsFn`, `expand_insufficient_topics`), but that logic is delegated to the Go
  engine via an injected callable (`engine/graph`/`engine/rpc`) and operates on
  `TopicCandidate`/`GraphNeighbor` gRPC-adjacent dataclasses -- not directly importable or
  reusable in-process for a Python-only, stdlib-only eval baseline. This subtask's own
  entity-graph traversal is a fresh, self-contained, in-memory implementation, but borrows the
  *design shape* from 4.4.2/4.4.3: capped hop count, explicit per-hop weighting (here: hop-decay
  scoring instead of a hard file-count cap, since this baseline scores/ranks rather than
  hard-capping a candidate list), and deterministic dedup.
- `agents/eval/datasets.py` (5.1.1) / `agents/eval/ground_truth.py` (5.1.3): both establish that
  `doc_id` is a plain, unnamespaced string matching `ingestion.rawdoc.RawDocument.id` /
  `ground_truth.RelevantDoc.doc_id`. This subtask's graph nodes/edges and `retrieve_documents`
  output use the same plain-string `doc_id` convention. Per 5.2.1's/5.2.2's own explicit scope
  boundary (reiterated in section 5 below), this subtask does not wire up `datasets.py` or
  `ground_truth.py`'s real corpus/labels -- it is fixture-only, per its own test spec.

## 3. Design chosen

**Entity extraction**: LLM-prompted (`llama3.1:8b` via `OllamaClient.complete`), asking for a
JSON array of short entity/concept strings (proper nouns, named systems, policy/topic names)
mentioned in a document or query. This is a genuine (non-strawman) extraction method -- not a
regex-only "capitalized word" heuristic, matching this run's fairness constraint #3 and mirroring
5.2.2's own choice of LLM-based reranking over a weaker keyword heuristic. A capitalized-multiword
heuristic fallback (`_heuristic_entities`) is used only when the model's JSON response is
completely unparseable (never as the primary path) -- pure robustness, disclosed here so the
verifier can confirm it's not silently becoming the primary extraction path in practice (it is
exercised in a pure-unit test with a deliberately garbled string, not the live-Ollama tests).

**Graph representation -- stdlib-only, no networkx**: a plain `dataclass EntityGraph` holding
three `dict[str, set[str]]` adjacency maps (entity->doc_ids, entity->co-occurring-entities,
doc_id->entities). No new dependency is added to `agents/pyproject.toml` (verified: still only
`fastapi/uvicorn/grpcio*/protobuf/pydantic/httpx/pymupdf` + dev `pytest/ruff`, matching 5.2.1's
"reuse existing httpx, no new embedding library" precedent extended to "no new graph library").
Entity keys are canonicalized (`.strip().lower()`) so the graph doesn't fragment on
casing/whitespace variation between separate LLM extraction calls (query text vs. document text),
while the original (first-seen) display casing is preserved in `EntityGraph.display_name` for any
future debug/inspection use -- this is disclosed as a real (if simple) entity-linking/dedup step,
not merely case-folding for its own sake.

**Graph construction**: for each `(doc_id, text)`, extract that document's entities; every pair of
entities extracted from the *same* document becomes a co-occurrence edge (undirected,
`entity_to_entities[a].add(b)` and vice versa) -- a standard, simple GraphRAG-style
entity-co-occurrence graph, matching common lightweight-GraphRAG designs (extract entities per
document, link entities that co-occur, rather than a full relation-extraction/knowledge-graph
pipeline, which would be well beyond fixture-scale/no-new-dependency scope).

**Query-time retrieval** (`retrieve_documents`): extract the query's own entities the same way;
match them against the graph (exact canonical match first; a substring-containment fallback in
either direction for near-miss phrasing, e.g. query entity "password reset" vs. document entity
"password rotation policy" sharing "password" -- disclosed as a deliberately loose, real
entity-linking heuristic, not an artificially strict match designed to make the arm look weak).
Matched entities contribute their linked `doc_id`s at hop-0 weight `1.0`; up to `max_hops`
(default `1`) further graph hops are then walked via `entity_to_entities`, with each additional hop
contributing at a strictly decayed weight (`0.5 ** hop`) -- this decay is the disclosed mitigation
for `docs/HLD.md` #7's "graph traversal context blow-up" risk (section 1 above): expansion still
contributes to recall (a paraphrased query entity that doesn't literally match a document entity
can still reach it via a shared co-occurring entity), but contributes less than a direct hit, so
it cannot on its own outrank a genuinely on-topic direct match. Document scores are the sum of all
matching-entity contributions; ranked descending, ties broken by `doc_id` ascending for
determinism; truncated to `top_k`. This exactly matches `vector_rag.retrieve_documents`'s
`list[str]` ranked-document-id output shape.

## 4. Fixture design

Per 5.2.1's/5.2.2's own precedent (each subtask ships its own dedicated, non-shared fixture,
engineered to discriminate the mechanism actually under test -- not reused verbatim from a prior
subtask's fixture, which was tuned for a different mechanism), this subtask's fixture corpus is
built around a small set of clearly-named entities (people, systems, policy names) with genuine
paraphrase distance between query wording and document wording, so that:
- A literal-keyword-only retrieval method would plausibly miss the paraphrased case.
- The entity-graph method must actually use graph co-occurrence expansion (not just direct
  entity-string matching) to recover at least one of the fixture queries -- proving the "build/
  query an entity graph" part of the acceptance criterion is genuinely exercised, not just
  "extract entities and do a literal string search."

## 5. Scope boundary (disclosed, matching 5.2.1/5.2.2 precedent)

Fixture-only: `agents/eval/test_graphrag_baseline.py` never imports `agents/eval/datasets.py` or
reads `data/synthetic_corpus/`; no real large-scale benchmark run. Wiring this baseline against
the real corpus + `ground_truth.py` labels is out of scope (reserved for a future subtask, e.g.
5.3.x, per the same reservation 5.2.1's own docstring makes for itself).

## 6. Environment check

Local Ollama server reachable at `http://localhost:11434` with both `llama3.1:8b` and
`nomic-embed-text:latest` already pulled (verified via `curl /api/tags` during this run) --
`nomic-embed-text` is not needed by this subtask (no embeddings used), only `llama3.1:8b` for
entity extraction, matching the LLM already used by 5.2.2's reranker.
