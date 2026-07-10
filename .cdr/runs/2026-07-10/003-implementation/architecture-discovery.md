# Architecture discovery

## SearchCandidates contract (proto/hivemind.proto, engine/rpc/server.go)

```
message SearchCandidatesRequest { string query = 1; int32 max_results = 2; }
message CandidateTopic { uint64 file_id = 1; string path = 2; float score = 3; }
message SearchCandidatesResponse { repeated CandidateTopic candidates = 1; }
rpc SearchCandidates(SearchCandidatesRequest) returns (SearchCandidatesResponse);
```

Server impl (`engine/rpc/server.go:302-331`) is a **btree prefix scan over topic path**,
not a content/semantic search: `btree.PrefixScan(store, root, req.GetQuery())`, bounded
by `max_results`, every result gets the same placeholder `score` constant
(`searchCandidateScore`). Empty-string query matches every key
(`strings.HasPrefix(key, "") == true` in `engine/btree/scan.go:56`), so
`SearchCandidates(query="", max_results=N)` is the mechanism to pull a bounded *pool* of
the catalog's topic paths — it does no document-content ranking itself.

`docs/LLD/ingestion-agent.md` (lines 34-37, 76-77) confirms the intended split: "The
shortlist comes from a cheap local heuristic (BM25 / a small local embedding) against
the candidate list from `engine/btree/`'s prefix-scan-style `SearchCandidates` lookup —
not the full catalog — to bound prompt size and reduce topic-name drift/duplication."
I.e. `SearchCandidates` supplies the bounded *pool*; the *content-aware ranking* is this
subtask's own local responsibility.

## Python gRPC client for the engine service

`agents/hivemind_pb2.py` + `agents/hivemind_pb2_grpc.py` (generated from
`proto/hivemind.proto` via `grpcio-tools`, already a project dependency in
`agents/pyproject.toml`) exist and define `HiveMindStub.SearchCandidates`. However, no
wrapper/client module in `agents/` currently constructs a channel + stub and calls it —
`grep -rn "HiveMindStub"` outside the two generated files returns nothing. This is a
real gap for this subtask to fill with a minimal, clearly-scoped client.

## Embedding/BM25 choice

`agents/pyproject.toml` has no embedding/NLP/vector dependency (`fastapi`, `uvicorn`,
`grpcio(-tools)`, `pydantic`, `httpx`, `pymupdf` only); `agents/llm/` (3.4.1) is a
text-completion client only, no embedding call. No embedding model is wired up anywhere
in the repo. Per the issue's own "prefer the simpler, dependency-light option" guidance:
implement Okapi BM25 in pure Python (no new dependency), matching the issue's literal
"BM25" alternative and the LLD's own wording.

## Sibling module conventions (`agents/ingestion/*.py`)

snake_case, frozen `@dataclass`, `from __future__ import annotations`, long structured
module/function docstrings disclosing design decisions, `ValueError` for caller-shape
mistakes, tests under `agents/ingestion/test_*.py` run via `agents/.venv`.
