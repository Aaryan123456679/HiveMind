"""Simplified GraphRAG-style baseline: entity-graph retrieval. Per issue #26/#27 (milestone #7,
"Phase 5"), subtask 5.2.3. `docs/LLD/eval.md` names this the third of three retrieval arms
compared against HiveMind's own topic-file/graph retrieval: "Simplified GraphRAG-style -- entity-
graph retrieval baseline." This module implements that arm: extract entities from documents/
queries, build/query a lightweight in-memory entity graph, and retrieve associated documents.

Fairness disclosure -- why not a strawman
------------------------------------------
Per this subtask's own instructions (mirroring 5.2.1's "real chunk size/overlap, not arbitrary"
standard and `docs/HLD.md` #7's "Benchmark fairness" known risk), a deliberately weak entity-graph
baseline -- e.g. a pure regex/capitalized-word heuristic with literal string matching only --
would make HiveMind's own graph-based retrieval look artificially better by comparison. This
module addresses that directly:

1. **Entity extraction is LLM-prompted, not regex-only.** `extract_entities` prompts the local
   Ollama completion model (`llama3.1:8b`, the same model 5.2.2's reranker uses) for a JSON list
   of named entities/concepts in a document or query. A capitalized-multiword heuristic
   (`_heuristic_entities`) exists only as a last-resort fallback when the model's JSON response
   is completely unparseable -- it is never the primary extraction path (see
   `parse_entity_list`'s docstring).
2. **Graph construction is real co-occurrence linking, not a flat entity->doc index.** Every pair
   of entities extracted from the same document becomes a graph edge (`EntityGraph.
   entity_to_entities`), so the "graph" is genuinely traversable, not merely an inverted index
   relabeled as a graph.
3. **Query-time retrieval genuinely traverses the graph.** `retrieve_documents` performs a
   capped-hop breadth-first walk over `entity_to_entities` (`max_hops`, default 1) so a query
   entity that does not literally appear in any document can still reach a relevant document via
   a shared co-occurring entity -- with each additional hop's contribution decayed
   (`HOP_DECAY ** hop`) so hop-expansion can improve recall without being able to outrank a direct
   entity match. This decay is this module's disclosed mitigation for `docs/HLD.md` #7's "graph
   traversal context blow-up" known risk (full corpus-scale precision-impact measurement is left
   to the future metrics pipeline, issue #28, per this module's own fixture-only scope below).

Stdlib-only graph representation -- disclosed choice, no new dependency
-------------------------------------------------------------------------
`agents/pyproject.toml` has no graph-library dependency (no `networkx`), matching 5.2.1's/5.2.2's
"no new heavyweight ML dependency" precedent extended here to graph libraries. `EntityGraph` is a
plain dataclass of `dict[str, set[str]]` adjacency maps -- a dict-of-sets undirected graph is
sufficient for fixture-scale corpora and this module's simple hop-decay scoring; a real-corpus
benchmark run (out of this subtask's scope, see below) might reasonably introduce `networkx` or
a dedicated graph database for scale, but that decision belongs to whichever future subtask wires
this baseline up to the real corpus.

Ollama-only, direct-instantiation pattern -- disclosed, matching established `agents/eval/`
precedent
-------------------------------------------------------------------------------------------
This module never imports `agents.llm.factory`, never reads an environment variable, and never
reads a `.env` file. Callers construct `llm.ollama_client.OllamaClient` directly (mirroring
5.2.2's `vector_rag_rerank.py`'s own precedent) and pass it in as the `llm_client` argument to
`extract_entities`/`retrieve_documents`/`EntityGraph.build` -- all of which are typed against the
provider-agnostic `llm.client.LLMClient` interface, not `OllamaClient` specifically, so no
Ollama-specific import lives inside this module itself. Entity extraction is a prompted,
single-shot text-in/text-out completion, which is exactly `LLMClient.complete`'s designed shape
(per 5.2.2's own disclosed reasoning for choosing prompted-LLM reranking over a bespoke client) --
so, like 5.2.2 and unlike 5.2.1's embeddings, no new client abstraction is needed here.

Robust JSON parsing -- reused, not reimplemented
----------------------------------------------------
`json_fences.strip_code_fences` / `json_fences.sanitize_control_chars_and_triple_quotes` are the
repo's already-hardened helpers for cleaning a local Ollama model's raw JSON completion string
before `json.loads` (established by `ingestion/segment.py`, subtask 3.4.6/4.5.17.2, against
observed `llama3.1:8b` failure modes: markdown code fences, raw control characters, stray
triple-quotes). This module reuses both unmodified rather than re-deriving the same
fence-stripping/sanitizing logic a third time.

Output-shape consistency with other baseline arms
------------------------------------------------------
`retrieve_documents(...) -> list[str]` returns ranked document ids, best-first, matching
`vector_rag.retrieve_documents`'s exact output shape (see that module's own docstring) so a future
metrics pipeline (issue #28, not yet built) can treat all baseline arms uniformly. This module
does not reimplement `recall_at_k`/`precision_at_k`; its own test file imports both directly from
`vector_rag`/`vector_rag_rerank` for consistency.

Scope boundary -- fixture-only, disclosed (mirrors 5.2.1/5.2.2)
---------------------------------------------------------------------
This module is corpus-agnostic (`EntityGraph.build` takes plain `(doc_id, text)` pairs, matching
`ingestion.rawdoc.RawDocument.id`/`.text`); wiring it up to `agents/eval/datasets.py`'s real
Bitext/Enron/synthetic-PDF corpus and `ground_truth.py`'s real labels for an actual benchmark run
is explicitly out of scope for this subtask (reserved for a future subtask) -- this subtask only
self-tests against its own inline fixture corpus and fixture queries, per its own test spec.
"""

from __future__ import annotations

import json
import re
from collections import deque
from dataclasses import dataclass, field

from json_fences import sanitize_control_chars_and_triple_quotes, strip_code_fences
from llm.client import LLMClient

#: Default local Ollama completion model used for entity extraction. Same model 5.2.2's
#: `vector_rag_rerank.py` uses for reranking -- see module docstring's "Ollama-only" section.
DEFAULT_LLM_MODEL = "llama3.1:8b"

#: Ollama's standard local-server default address (informational only -- this module never
#: constructs an `OllamaClient` itself; callers do, per the module docstring's disclosed
#: direct-instantiation pattern).
DEFAULT_BASE_URL = "http://localhost:11434"

#: Default maximum number of graph hops walked outward from a directly-matched query entity
#: during retrieval. See module docstring's "graph traversal context blow-up" mitigation section.
DEFAULT_MAX_HOPS = 1

#: Per-hop score decay factor: a hop-`n` entity match contributes `HOP_DECAY ** n` to a
#: document's score (hop 0 == a direct entity match contributes the full weight of `1.0`). See
#: module docstring's disclosed blow-up mitigation.
HOP_DECAY = 0.5

#: Heuristic fallback (see `_heuristic_entities`) only fires on capitalized multi-word runs at
#: least this long, to avoid extracting single common capitalized words (e.g. a sentence's first
#: word) as spurious "entities."
_MIN_HEURISTIC_ENTITY_WORDS = 1

_CAPITALIZED_RUN_RE = re.compile(r"\b[A-Z][a-zA-Z0-9]*(?:\s+[A-Z][a-zA-Z0-9]*)*\b")

#: Common sentence-leading words that are capitalized purely by virtue of starting a sentence,
#: not because they are part of a named entity -- stripped from the front of a heuristic
#: candidate so e.g. "The Security Team" (sentence-initial "The") yields "Security Team", not a
#: spurious partial entity. Only applied when the candidate has more than one word, so a
#: genuinely single-word entity that happens to be one of these (unlikely, but not this
#: heuristic's job to guess) is left alone.
_LEADING_STOPWORDS = {"the", "a", "an", "this", "that", "these", "those"}


def build_entity_extraction_prompt(text: str) -> str:
    """Build a single entity-extraction prompt for `text`.

    Instructs the model to return ONLY a JSON array of short entity/concept strings (proper
    nouns, named systems, policy or topic names) mentioned in the text -- nothing else -- so the
    response is directly `json.loads`-able (after `parse_entity_list`'s fence-stripping/
    sanitizing pass).
    """
    return (
        "Extract the key entities, concepts, and topics mentioned or asked about in the text "
        "below, whether the text is a statement or a question. Include proper nouns (named "
        "systems, teams, people, policy names) AND important common noun phrases naming a "
        "topic, process, or thing (for example \"password reset\", \"account access\", "
        "\"badge access\", \"multi-factor authentication\") -- do not limit yourself to formal "
        "proper nouns only.\n\n"
        "Example.\n"
        "Text: Who approves exceptions to the multi-factor authentication requirement?\n"
        "Answer: [\"multi-factor authentication\", \"exception approval\"]\n\n"
        "Every input text names at least one entity/concept/topic -- never respond with an "
        "empty array. Respond with ONLY a JSON array of short strings and nothing else -- no "
        "explanation, no markdown formatting.\n\n"
        f"Text:\n{text}\n"
    )


def _heuristic_entities(source_text: str) -> list[str]:
    """Last-resort, non-primary entity-extraction fallback (see module docstring, fairness point
    1): capitalized-word/multi-word-run extraction, used only when the LLM's JSON response is
    completely unparseable by `parse_entity_list`.
    """
    candidates = _CAPITALIZED_RUN_RE.findall(source_text)
    cleaned: list[str] = []
    for candidate in candidates:
        words = candidate.split()
        if len(words) > 1 and words[0].lower() in _LEADING_STOPWORDS:
            words = words[1:]
        if len(words) >= _MIN_HEURISTIC_ENTITY_WORDS:
            cleaned.append(" ".join(words))
    return cleaned


def parse_entity_list(response_text: str, source_text: str = "") -> list[str]:
    """Parse an LLM's entity-extraction response into a deduplicated list of entity strings.

    Applies `json_fences.strip_code_fences` then `json_fences.
    sanitize_control_chars_and_triple_quotes` (both reused unmodified, see module docstring)
    before `json.loads`, tolerating the same markdown-fence/control-character/triple-quote
    artifacts `ingestion/segment.py` already tolerates from real `llama3.1:8b` completions.

    On any parse failure, or if the parsed JSON is not a list of strings, falls back to
    `_heuristic_entities(source_text)` -- this fallback is disclosed as non-primary (see module
    docstring's fairness section); `source_text` should be the original document/query text
    being extracted from (empty string yields an empty fallback list, e.g. for isolated
    unit tests of parsing behavior alone).

    Returns entities deduplicated case-insensitively (first-seen casing preserved), with
    surrounding whitespace stripped and empty strings dropped. Order is first-seen order.
    """
    cleaned = sanitize_control_chars_and_triple_quotes(strip_code_fences(response_text))
    parsed: object
    try:
        parsed = json.loads(cleaned)
    except (json.JSONDecodeError, ValueError):
        parsed = None

    raw_entities: list[str]
    if isinstance(parsed, list) and all(isinstance(item, str) for item in parsed):
        raw_entities = parsed
    else:
        raw_entities = _heuristic_entities(source_text)

    seen: set[str] = set()
    deduped: list[str] = []
    for entity in raw_entities:
        stripped = entity.strip()
        if not stripped:
            continue
        key = stripped.lower()
        if key in seen:
            continue
        seen.add(key)
        deduped.append(stripped)
    return deduped


def extract_entities(text: str, llm_client: LLMClient, *, model: str | None = None) -> list[str]:
    """Extract entities from `text` via `llm_client` (see module docstring's Ollama-only
    section for the disclosed direct-instantiation calling pattern).

    Args:
        text: Document or query text to extract entities from.
        llm_client: Any `LLMClient` implementation (production callers pass a directly
            constructed `llm.ollama_client.OllamaClient`; tests may pass a stub).
        model: Optional per-call model override; defaults to `llm_client`'s own configured
            default model (typically `DEFAULT_LLM_MODEL`, i.e. `"llama3.1:8b"`).

    Returns:
        A deduplicated list of entity strings, per `parse_entity_list`.
    """
    if not text or not text.strip():
        return []
    response = llm_client.complete(
        build_entity_extraction_prompt(text), model=model, temperature=0.0
    )
    return parse_entity_list(response, text)


def _canonical(entity: str) -> str:
    """Canonicalize an entity string to a graph node key: stripped, lowercased.

    See module docstring's "entity canonicalization" note: extraction is called separately for
    each document/query, so canonicalization is what lets e.g. `"Password Policy"` (from one
    document) and `"password policy"` (from the query, or another document) resolve to the same
    graph node rather than fragmenting the graph.
    """
    return entity.strip().lower()


@dataclass
class EntityGraph:
    """An in-memory, stdlib-only entity co-occurrence graph (see module docstring's "no new
    dependency" disclosure).

    All three adjacency maps are keyed by canonical (`_canonical`) entity strings:

    - `entity_to_docs`: canonical entity -> set of `doc_id`s whose text it was extracted from.
    - `entity_to_entities`: canonical entity -> set of canonical entities it co-occurred with in
      at least one document (undirected: if `a` links to `b`, `b` links to `a`).
    - `doc_entities`: `doc_id` -> set of canonical entities extracted from that document.

    `display_name` maps each canonical entity back to its first-seen original (non-canonical)
    casing, for any future debug/inspection use; retrieval itself only operates on canonical
    keys.
    """

    entity_to_docs: dict[str, set[str]] = field(default_factory=dict)
    entity_to_entities: dict[str, set[str]] = field(default_factory=dict)
    doc_entities: dict[str, set[str]] = field(default_factory=dict)
    display_name: dict[str, str] = field(default_factory=dict)

    @classmethod
    def build(
        cls,
        docs: list[tuple[str, str]],
        llm_client: LLMClient,
        *,
        model: str | None = None,
    ) -> "EntityGraph":
        """Build an `EntityGraph` over `docs` (`(doc_id, text)` pairs, matching
        `ingestion.rawdoc.RawDocument.id`/`.text`'s shape -- see module docstring's scope
        boundary).

        Extracts entities per document via `extract_entities`, then links every pair of
        entities extracted from the *same* document as a co-occurrence edge (module docstring,
        fairness point 2).
        """
        graph = cls()
        for doc_id, text in docs:
            entities = extract_entities(text, llm_client, model=model)
            canonical_entities: set[str] = set()
            for entity in entities:
                key = _canonical(entity)
                if not key:
                    continue
                canonical_entities.add(key)
                graph.display_name.setdefault(key, entity)
                graph.entity_to_docs.setdefault(key, set()).add(doc_id)
                graph.entity_to_entities.setdefault(key, set())

            graph.doc_entities[doc_id] = canonical_entities

            for a in canonical_entities:
                for b in canonical_entities:
                    if a != b:
                        graph.entity_to_entities[a].add(b)

        return graph


def _match_query_entities(query_entities: list[str], graph: EntityGraph) -> set[str]:
    """Resolve extracted `query_entities` to canonical graph node keys.

    Exact canonical match first; for any query entity with no exact match, falls back to a
    substring-containment check (either direction) against every known graph entity key -- a
    deliberately loose, disclosed entity-linking heuristic (module docstring's fairness section)
    so a paraphrased query entity (e.g. "password reset" vs. a document's "password rotation
    policy", sharing "password") can still resolve to a real graph node, rather than requiring
    brittle exact-string equality that would make this arm an artificially weak strawman.
    """
    known_keys = list(graph.entity_to_docs)
    matched: set[str] = set()
    for entity in query_entities:
        key = _canonical(entity)
        if not key:
            continue
        if key in graph.entity_to_docs:
            matched.add(key)
            continue
        for known in known_keys:
            if key in known or known in key:
                matched.add(known)
    return matched


def retrieve_documents(
    query: str,
    graph: EntityGraph,
    llm_client: LLMClient,
    *,
    top_k: int,
    max_hops: int = DEFAULT_MAX_HOPS,
    model: str | None = None,
) -> list[str]:
    """Retrieve ranked document ids for `query` via entity-graph traversal.

    Extracts `query`'s own entities (via `extract_entities`), resolves them against `graph`
    (`_match_query_entities`), then walks outward from each matched entity up to `max_hops` hops
    over `graph.entity_to_entities`, scoring every reached document by the sum, over all
    matching entities, of `HOP_DECAY ** hop` (hop 0 == a directly matched entity). Documents are
    ranked by descending score, ties broken by ascending `doc_id` for determinism, and truncated
    to `top_k`.

    Matches `vector_rag.retrieve_documents`'s `list[str]` ranked-document-id output shape (see
    module docstring's "output-shape consistency" section).

    Args:
        query: Query text.
        graph: An `EntityGraph` built via `EntityGraph.build`.
        llm_client: `LLMClient` used to extract the query's own entities (should typically be
            the same client used to build `graph`, though this is not enforced).
        top_k: Number of ranked document ids to return.
        max_hops: Maximum number of additional graph hops walked outward from each directly
            matched query entity. `0` disables hop expansion entirely (direct entity matches
            only). Defaults to `DEFAULT_MAX_HOPS` (`1`).
        model: Optional per-call model override forwarded to `extract_entities`.

    Returns:
        Up to `top_k` document ids, best-first, each document at most once.
    """
    query_entities = extract_entities(query, llm_client, model=model)
    matched_roots = _match_query_entities(query_entities, graph)

    # BFS outward from every matched root entity, tracking the minimum hop distance at which
    # each entity is reached (an entity reachable from multiple roots, or via multiple paths,
    # counts at its shortest distance only -- avoids double-counting the same entity's
    # contribution to a document's score).
    hop_distance: dict[str, int] = {root: 0 for root in matched_roots}
    frontier: deque[tuple[str, int]] = deque((root, 0) for root in matched_roots)
    while frontier:
        entity, hop = frontier.popleft()
        if hop >= max_hops:
            continue
        for neighbor in graph.entity_to_entities.get(entity, set()):
            if neighbor not in hop_distance:
                hop_distance[neighbor] = hop + 1
                frontier.append((neighbor, hop + 1))

    scores: dict[str, float] = {}
    for entity, hop in hop_distance.items():
        weight = HOP_DECAY**hop
        for doc_id in graph.entity_to_docs.get(entity, set()):
            scores[doc_id] = scores.get(doc_id, 0.0) + weight

    ranked_doc_ids = sorted(scores, key=lambda doc_id: (-scores[doc_id], doc_id))
    return ranked_doc_ids[:top_k]
