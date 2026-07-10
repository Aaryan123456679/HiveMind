"""Candidate topic shortlisting: bounded `SearchCandidates` pool + local BM25 pre-filter.

Per issue #18 subtask 3.4.2 and `docs/LLD/ingestion-agent.md` ("The shortlist comes
from a cheap local heuristic ... against the candidate list from `engine/btree/`'s
prefix-scan-style `SearchCandidates` lookup -- not the full catalog -- to bound prompt
size and reduce topic-name drift/duplication"): the (not-yet-built, 3.4.3) segmentation
prompt must never be handed the full topic catalog. This module produces a small,
content-relevant subset instead.

Division of labor between the RPC and local filtering -- disclosed design
---------------------------------------------------------------------------
`SearchCandidates` (see `proto/hivemind.proto`, `engine/rpc/server.go`) is a **btree
prefix scan over topic *path*, not a content/semantic search**: the server calls
`btree.PrefixScan(store, root, req.GetQuery())`, bounded by `max_results`, and every
result gets the same placeholder score. It has no way to rank by document content --
it only takes a path-prefix `query` string and a result cap.

So the two responsibilities split cleanly:

- The RPC call supplies a **bounded pool**: this module calls it with
  ``query=""`` (an empty prefix matches every stored key --
  ``strings.HasPrefix(key, "")`` is always true in `engine/btree/scan.go`) and
  ``max_results=pool_size``, i.e. "give me up to `pool_size` topics from the
  catalog." This is itself a bound -- even before local filtering, the pool handed to
  the next step can never exceed `pool_size`.
- **This module** then re-ranks that pool locally, against the actual document text,
  using Okapi BM25 over each candidate topic path's tokens, and truncates to
  `top_k`. This is the "cheap ... pre-filter" the issue asks for, and is what actually
  makes the shortlist *relevant* (not just *small*) -- the RPC alone cannot do this.

Embedding vs. BM25 -- disclosed choice
-----------------------------------------
The issue allows either "a cheap embedding/BM25 pre-filter." `agents/pyproject.toml`
has no embedding/vector dependency, and no embedding model is wired up anywhere else
in this repo (`agents/llm/`, task 3.4.1, is a text-completion client only). Per the
issue's own "prefer the simpler, dependency-light option" guidance, this module
implements Okapi BM25 in pure Python: no new third-party dependency, deterministic,
and directly matches the issue's own alternative wording and the LLD's "cheap local
heuristic" framing.

Python gRPC client for `SearchCandidates` -- disclosed gap
--------------------------------------------------------------
`agents/hivemind_pb2.py` / `agents/hivemind_pb2_grpc.py` (generated from
`proto/hivemind.proto` via the already-declared `grpcio-tools` dependency) define
`HiveMindStub.SearchCandidates`, but no wrapper anywhere in `agents/` actually
constructs a channel + stub and calls it -- this is a real, previously-unfilled gap.
:class:`GrpcSearchCandidatesClient` fills it minimally: a thin callable wrapping
`HiveMindStub` over a caller-supplied `grpc.Channel`, translating the generated
`CandidateTopic` message into this module's plain :class:`TopicCandidate`. `grpc` /
the generated stubs are imported lazily inside the class (not at module import time),
so importing/testing `shortlist()` itself never requires `grpc` to be importable or a
channel/engine to exist -- `test_shortlist.py` mocks `SearchCandidates` entirely via a
plain Python callable, per the issue's test spec, and never touches this class's real
network path.
"""

from __future__ import annotations

import math
import re
from dataclasses import dataclass
from typing import TYPE_CHECKING, Callable, Sequence

if TYPE_CHECKING:
    import grpc

#: Default size of the bounded pool requested from `SearchCandidates` before local
#: BM25 re-ranking. This itself bounds worst-case work/memory even before `top_k`
#: truncation -- the shortlist is never unboundedly large regardless of catalog size.
DEFAULT_POOL_SIZE = 200

#: Default number of topics returned by `shortlist()` -- the bound the segmentation
#: prompt (3.4.3) actually sees.
DEFAULT_TOP_K = 8

#: Standard Okapi BM25 free parameters.
_BM25_K1 = 1.5
_BM25_B = 0.75

_TOKEN_SPLIT_RE = re.compile(r"[^a-zA-Z0-9]+")
_CAMEL_BOUNDARY_RE = re.compile(r"(?<=[a-z0-9])(?=[A-Z])")


@dataclass(frozen=True)
class TopicCandidate:
    """A single candidate topic, as returned by `SearchCandidates` or `shortlist()`.

    Mirrors `proto/hivemind.proto`'s `CandidateTopic` message shape (`file_id`,
    `path`, `score`), but is this module's own plain dataclass rather than the
    generated protobuf type, so callers/tests never need `grpc`/the generated stubs
    importable just to construct or compare candidates.

    `score`'s meaning differs by where the value came from: raw pool entries (as
    handed to `shortlist()` from a `SearchCandidatesFn`) carry whatever placeholder
    score the engine assigned (frequently a constant -- the engine's `SearchCandidates`
    does not rank by content); entries returned *from* `shortlist()` always carry this
    module's own BM25 relevance score instead.
    """

    file_id: int
    path: str
    score: float


#: The injection point `shortlist()` takes for calling `SearchCandidates`: given
#: `(query, max_results)`, return the raw candidate pool. Mirrors
#: `SearchCandidatesRequest{query, max_results}` -> a sequence of `TopicCandidate`.
#: Tests supply a plain mock callable here; `GrpcSearchCandidatesClient` below is the
#: real (gRPC-backed) implementation.
SearchCandidatesFn = Callable[[str, int], Sequence[TopicCandidate]]


def _tokenize(text: str) -> list[str]:
    """Lowercase word tokens from `text`, splitting on non-alphanumerics, path
    separators, `_`/`-`, and camelCase boundaries.

    E.g. `"billing/InvoiceDisputes"` -> `["billing", "invoice", "disputes"]`, so a
    topic path's tokens overlap with the plain-English words a document about that
    topic would actually contain.
    """
    with_camel_split = _CAMEL_BOUNDARY_RE.sub(" ", text)
    return [tok.lower() for tok in _TOKEN_SPLIT_RE.split(with_camel_split) if tok]


def _bm25_scores(query_tokens: Sequence[str], docs_tokens: Sequence[Sequence[str]]) -> list[float]:
    """Okapi BM25 score of `query_tokens` against each entry in `docs_tokens`.

    Standard formula: for each query term t, add
    ``idf(t) * freq(t, d) * (k1 + 1) / (freq(t, d) + k1 * (1 - b + b * |d| / avgdl))``.
    `idf(t) = ln((N - n(t) + 0.5) / (n(t) + 0.5) + 1)`, where `N` is the number of
    documents and `n(t)` the number containing `t`. Returns one float per document, in
    `docs_tokens` order (not sorted -- callers sort).
    """
    n_docs = len(docs_tokens)
    if n_docs == 0:
        return []

    doc_lengths = [len(doc) for doc in docs_tokens]
    avg_doc_len = sum(doc_lengths) / n_docs if n_docs else 0.0

    unique_query_terms = set(query_tokens)
    doc_freq: dict[str, int] = {
        term: sum(1 for doc in docs_tokens if term in doc) for term in unique_query_terms
    }
    idf = {
        term: math.log((n_docs - df + 0.5) / (df + 0.5) + 1.0)
        for term, df in doc_freq.items()
    }

    scores: list[float] = []
    for doc, doc_len in zip(docs_tokens, doc_lengths):
        if doc_len == 0:
            scores.append(0.0)
            continue
        score = 0.0
        for term in unique_query_terms:
            term_freq = doc.count(term)
            if term_freq == 0:
                continue
            denom = term_freq + _BM25_K1 * (1 - _BM25_B + _BM25_B * doc_len / avg_doc_len)
            score += idf[term] * (term_freq * (_BM25_K1 + 1)) / denom
        scores.append(score)
    return scores


def shortlist(
    document_text: str,
    search_candidates: SearchCandidatesFn,
    *,
    top_k: int = DEFAULT_TOP_K,
    pool_size: int = DEFAULT_POOL_SIZE,
) -> list[TopicCandidate]:
    """Return a bounded, content-relevant shortlist of candidate topics.

    Args:
        document_text: The document's text (or a representative excerpt of it) that
            the shortlist should be relevant to.
        search_candidates: Callable satisfying `SearchCandidatesFn` -- called once as
            `search_candidates("", pool_size)` to fetch a bounded raw pool from the
            engine's `SearchCandidates` RPC (empty-prefix query matches every stored
            topic path; see module docstring for why the RPC itself cannot rank by
            content). Tests mock this directly; `GrpcSearchCandidatesClient` is the
            real gRPC-backed implementation.
        top_k: Maximum number of candidates returned. Must be >= 0.
        pool_size: Maximum size of the raw pool requested from `search_candidates`
            before local re-ranking. Must be >= 0.

    Returns:
        Up to `top_k` `TopicCandidate`s from the pool, sorted by descending local
        BM25 relevance to `document_text` (ties broken by the pool's original
        order, for determinism). Never larger than `min(top_k, pool_size)`, and
        never larger than the pool actually returned by `search_candidates` --
        i.e. always bounded, never the full catalog.

    Raises:
        ValueError: If `top_k` or `pool_size` is negative.
    """
    if top_k < 0:
        raise ValueError(f"shortlist: top_k must be >= 0, got {top_k}")
    if pool_size < 0:
        raise ValueError(f"shortlist: pool_size must be >= 0, got {pool_size}")

    pool = list(search_candidates("", pool_size))
    if top_k == 0 or not pool:
        return []

    query_tokens = _tokenize(document_text)
    docs_tokens = [_tokenize(candidate.path) for candidate in pool]
    scores = _bm25_scores(query_tokens, docs_tokens)

    ranked_indices = sorted(range(len(pool)), key=lambda i: (-scores[i], i))
    top_indices = ranked_indices[:top_k]

    return [
        TopicCandidate(file_id=pool[i].file_id, path=pool[i].path, score=scores[i])
        for i in top_indices
    ]


def _import_hivemind_grpc_modules():
    """Import and return `(hivemind_pb2, hivemind_pb2_grpc)`, falling back to adding
    `agents/`'s absolute path onto `sys.path` if the plain top-level import fails (see
    `GrpcSearchCandidatesClient`'s docstring for why that fallback is needed).
    """
    try:
        import hivemind_pb2
        import hivemind_pb2_grpc
    except ImportError:
        import sys
        from pathlib import Path

        agents_dir = str(Path(__file__).resolve().parent.parent)
        if agents_dir not in sys.path:
            sys.path.insert(0, agents_dir)
        import hivemind_pb2
        import hivemind_pb2_grpc
    return hivemind_pb2, hivemind_pb2_grpc


class GrpcSearchCandidatesClient:
    """Minimal real `SearchCandidates` client, filling the disclosed Python-gRPC-client
    gap: wraps the generated `hivemind_pb2_grpc.HiveMindStub` over a caller-supplied
    `grpc.Channel` (e.g. `grpc.insecure_channel("localhost:PORT")`), and is itself a
    valid `SearchCandidatesFn` (callable with `(query, max_results)`).

    `grpc`/the generated stubs are imported lazily in `__init__`/`__call__`, not at
    module import time, so importing `ingestion.shortlist` (and running
    `test_shortlist.py`, which never constructs this class against a real channel)
    never requires `grpc` to be importable or an engine instance to exist.

    `hivemind_pb2.py`/`hivemind_pb2_grpc.py` are plain top-level modules generated
    directly into `agents/` (not part of any package declared in
    `agents/pyproject.toml`'s `[tool.setuptools] packages`), and `hivemind_pb2_grpc.py`
    itself does a flat `import hivemind_pb2` -- both are only importable when
    `agents/` itself is on `sys.path`, which is not guaranteed just because
    `ingestion` (an installed package) is importable. `_import_hivemind_grpc_modules`
    falls back to inserting `agents/`'s absolute path onto `sys.path` if the plain
    import fails, so this class works regardless of how the caller's process was
    started.
    """

    def __init__(self, channel: "grpc.Channel") -> None:
        _, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

        self._stub = hivemind_pb2_grpc.HiveMindStub(channel)

    def __call__(self, query: str, max_results: int) -> list[TopicCandidate]:
        hivemind_pb2, _ = _import_hivemind_grpc_modules()

        request = hivemind_pb2.SearchCandidatesRequest(query=query, max_results=max_results)
        response = self._stub.SearchCandidates(request)
        return [
            TopicCandidate(file_id=c.file_id, path=c.path, score=c.score)
            for c in response.candidates
        ]
