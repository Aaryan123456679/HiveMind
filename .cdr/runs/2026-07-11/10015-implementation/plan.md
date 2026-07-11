# Plan -- Subtask 5.2.3

## `agents/eval/baselines/graphrag_lite.py`

1. Module docstring: purpose, fairness disclosure, stdlib-only-graph disclosure (why no
   networkx), Ollama-direct-instantiation-pattern disclosure, scope boundary -- mirroring
   5.2.1/5.2.2's docstring conventions.
2. `DEFAULT_LLM_MODEL = "llama3.1:8b"`, `DEFAULT_BASE_URL = "http://localhost:11434"` constants
   (mirroring 5.2.1/5.2.2's own constant style).
3. `DEFAULT_MAX_HOPS = 1`, `HOP_DECAY = 0.5` constants (documented rationale: blow-up mitigation).
4. `build_entity_extraction_prompt(text: str) -> str` -- asks for a JSON array of short entity
   strings, nothing else.
5. `_heuristic_entities(text: str) -> list[str]` -- capitalized-multiword fallback, private,
   only used when JSON parse totally fails.
6. `parse_entity_list(response_text: str, source_text: str) -> list[str]` -- strip_code_fences +
   sanitize_control_chars_and_triple_quotes (reused from `json_fences`) then `json.loads`;
   validates it's a list of strings; falls back to `_heuristic_entities(source_text)` on any
   parse/validation failure. Dedups case-insensitively, preserves first-seen casing, strips
   whitespace, drops empty strings.
7. `extract_entities(text: str, llm_client: LLMClient, *, model: str | None = None) -> list[str]`
   -- calls `llm_client.complete(build_entity_extraction_prompt(text), model=model,
   temperature=0.0)`, then `parse_entity_list`.
8. `_canonical(entity: str) -> str` -- `.strip().lower()`.
9. `@dataclass EntityGraph`: `entity_to_docs: dict[str, set[str]]`,
   `entity_to_entities: dict[str, set[str]]`, `doc_entities: dict[str, set[str]]` (canonical
   keys), `display_name: dict[str, str]` (canonical -> first-seen original casing).
   `classmethod build(cls, docs: list[tuple[str, str]], llm_client: LLMClient, *, model=None) ->
   EntityGraph` -- extract per doc, populate all four maps, add co-occurrence edges pairwise
   within each doc's entity set (excluding self-loops).
10. `_match_query_entities(query_entities: list[str], graph: EntityGraph) -> set[str]` --
    canonical-exact match first; substring-containment fallback (either direction) against
    `graph.entity_to_docs` keys for unmatched query entities.
11. `retrieve_documents(query: str, graph: EntityGraph, llm_client: LLMClient, *, top_k: int,
    max_hops: int = DEFAULT_MAX_HOPS, model: str | None = None) -> list[str]` -- extract query
    entities, match against graph, BFS outward up to `max_hops` over `entity_to_entities`
    tracking hop distance per reached entity (min distance if reached multiple ways), score
    documents by summed `HOP_DECAY ** hop` contributions from every matching entity, rank
    descending with `doc_id` ascending tie-break, truncate to `top_k`.
12. Re-export note in docstring: `recall_at_k` (from `vector_rag`) and `precision_at_k` (from
    `vector_rag_rerank`) are reused, not reimplemented, by the test file directly (no re-export
    needed in this module itself since the test file can import both directly).

## `agents/eval/test_graphrag_baseline.py`

Mirrors `test_vector_rag_rerank.py`'s two-tier structure:
- Pure-unit tests (no network): `parse_entity_list` (valid JSON, fenced JSON, garbled ->
  heuristic fallback, dedup/casing), `EntityGraph.build`-adjacent helper behavior via a fake
  `LLMClient` stub (deterministic canned responses, no real Ollama call), `_match_query_entities`
  behavior indirectly through `retrieve_documents` with a stub client.
- Live-local tests (skipped if Ollama/`llama3.1:8b` unreachable, mirroring
  `_ollama_embeddings_available`-style check but for the completion model): build a real
  `EntityGraph` via the real local `OllamaClient`, run `retrieve_documents` against a fixture
  corpus/query set engineered so plain-substring keyword search would plausibly miss a
  paraphrased query, and the entity graph's co-occurrence hop expansion is required to recover
  it. Assert:
  - top hit for an unambiguous query is the correct doc.
  - `recall_at_k` (imported from `vector_rag`) across the fixture query set is "plausible"
    (>= 0.6, deliberately a looser bar than 5.2.1's 0.75 -- entity-graph retrieval over an LLM
    extraction step is a fuzzier signal than dense embeddings, and the test spec only asks for
    "plausible", not "reasonable/near-perfect").
  - at least one query is only recoverable via hop expansion (max_hops=1) and not via hop-0-only
    (max_hops=0) -- proving the "build/query an entity graph" (not just literal entity-string
    matching) part of the acceptance criterion is genuinely exercised.

## Self-consistency checks (after implementation)

- `ruff check agents/eval/baselines/graphrag_lite.py agents/eval/test_graphrag_baseline.py`
- `python -m pytest agents/eval/test_graphrag_baseline.py -v` (from `agents/`, live Ollama
  reachable in this environment) -- all pass, 0 unexpected skips.
- `python -m pytest agents/eval/ -q` -- full existing suite, no regressions.
- `git diff --stat` -- confirm only the two new files, `agents/pyproject.toml` untouched.
