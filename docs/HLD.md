---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# HiveMind — High-Level Design

## 1. Project identity

HiveMind is a self-organizing, file-based knowledge brain intended as a RAG replacement. Instead
of chunking documents into a vector DB, an LLM parses each incoming document once at ingestion
time and segregates its information into topic-coherent `.md` files (not vector chunks). Files
auto-split when they grow too large. Topics are linked via a traversable metadata knowledge graph.
Retrieval at query time uses a two-agent pipeline (intent-refiner + graph-aware topic-selector
with a tunable `k` hyperparameter for the number of topics). The purpose of the project is to
demonstrate both ML/agentic engineering and distributed-systems/storage-engine engineering,
benchmarked against classic vector RAG and GraphRAG-style baselines, plus a deployed demo with
load-test results.

## 2. Stack decisions

- **Go** for the storage/concurrency core (`engine/`) and the HTTP gateway (`api/`).
- **Python** for the ML/agent layer (`agents/`).
- A **custom on-disk storage engine** (own catalog, indexing, locking) — explicitly NOT
  Postgres/SQLite. This is the primary distributed-systems credibility piece of the project.
- **Single-node, multi-threaded** concurrency target (no multi-node sharding/consensus).
- **Ingestion LLM**: a local open-weight model via Ollama (e.g. Llama 3.1 8B), chosen for cost
  reasons given the high call volume at ingestion time.
- **Query-time agents** (intent-refiner, topic-selector, answer synthesizer): hosted small models
  — GPT-4o-mini via OpenRouter, or Gemini 2.5/3.5 Flash — behind a provider-agnostic LLM client
  interface in `agents/llm/`.
- **Dataset**: enterprise-style mixed docs — a public support-ticket dataset (e.g. Bitext), an
  Enron email subsample, and synthetic policy/manual PDFs seeded with ~30-50 predefined
  ground-truth topics and deliberate cross-topic references.

## 3. Architecture

```
Client/UI (React) -> API Gateway (Go, api/) -> gRPC -> Go Storage Engine (engine/)
                                              -> gRPC -> Python ML/Agent Service (agents/) -> LLM Provider Layer (Ollama | OpenRouter | Gemini)
```

Go and Python communicate over **gRPC, not REST**, specifically so per-call latency/cost can be
logged via interceptors on both sides for the benchmark. The call graph is bidirectional: the API
gateway calls into both the engine and the agent service for normal traffic, and the engine's
`split` module calls back into the agent service's `ProposeSplit` RPC during auto-split.

Per-module design detail lives in [docs/LLD/](LLD/); this document stays at the system level and
cross-links rather than duplicating.

### 3.1 Go Storage Engine (`engine/`)

| Module | Responsibility | LLD |
|---|---|---|
| `catalog/` | On-disk metadata catalog, slotted 4KB pages, striped-mutex concurrency | [LLD/catalog.md](LLD/catalog.md) |
| `btree/` | On-disk B+Tree mapping topic paths to fileIDs, latch-crabbing + optimistic reads | [LLD/btree.md](LLD/btree.md) |
| `mvcc/` | Per-file multi-version concurrency control, epoch-based GC | [LLD/mvcc.md](LLD/mvcc.md) |
| `split/` | Auto-split orchestration when a file exceeds its size threshold | [LLD/split.md](LLD/split.md) |
| `graph/` | Adjacency store + traversal API over topic/file relationships | [LLD/graph.md](LLD/graph.md) |
| `wal/` | Write-ahead log + checkpointing + crash recovery | [LLD/wal.md](LLD/wal.md) |
| `rpc/` | gRPC server exposing engine operations, and client of the agent's `ProposeSplit` | [LLD/rpc.md](LLD/rpc.md) |
| `loadtest/` | Concurrency/load-generation harness for benchmarking | [LLD/eval.md](LLD/eval.md) (harness shared with benchmark discussion) |

`api/` is a separate Go module: the HTTP gateway providing auth (simple token), rate limiting, and
routes `/ingest /query /graph /files /admin` that fan out to the engine and the agent service via
gRPC.

### 3.2 Python ML/Agent Service (`agents/`)

| Module | Responsibility | LLD |
|---|---|---|
| `ingestion/` | Per-doc-type normalizers + segmentation agent + `ProposeSplit` | [LLD/ingestion-agent.md](LLD/ingestion-agent.md) |
| `query/` | Intent-refiner, topic-selector, synthesizer | [LLD/query-agent.md](LLD/query-agent.md) |
| `llm/` | Provider-agnostic LLM client interface (Ollama / OpenRouter / Gemini) | [LLD/llm-provider.md](LLD/llm-provider.md) |
| `eval/` | Benchmark harness against vector-RAG and GraphRAG baselines | [LLD/eval.md](LLD/eval.md) |

## 4. Repo layout

```
HiveMind/
  engine/    # Go module: catalog, btree, graph, mvcc, split, wal, rpc, loadtest
  agents/    # Python: ingestion/, query/, llm/, eval/
  api/       # Go module: HTTP gateway
  ui/        # React dashboard (not yet built)
  data/      # dataset prep scripts (not yet built)
  deploy/    # Dockerfiles/compose, k8s manifests, OCI provisioning (issue #31, no longer a placeholder)
  proto/     # shared .proto for Go<->Python gRPC (not yet written)
```

## 5. Build-phase roadmap

See [AGENT.md](../AGENT.md) for the full ~6-8 week phase breakdown (storage core -> concurrency +
auto-split -> graph + ingestion agents -> query pipeline -> benchmark suite -> demo/deploy ->
buffer/polish). This HLD describes the target end-state architecture; AGENT.md tracks how we get
there.

## 6. Why this exists (novelty vs. prior art)

- **GraphRAG**: builds graphs over entities extracted from chunks, but retrieval still returns
  chunk/summary text — there is no living, self-splitting document store underneath it.
- **RAPTOR**: does static hierarchical clustering at index-build time. There's no incremental
  reorganization, and its tree nodes are LLM summaries, not original content.
- **MemGPT/Letta**: page hierarchical memory for a single agent's context window — not a shared,
  graph-linked, browsable multi-user corpus.
- **HiveMind's contribution**: the storage engine itself enforces topical coherence and
  consistency (via MVCC and auto-split) under concurrent access, combined with a learned/agentic
  graph-aware retrieval step that replaces embedding similarity search — explicitly evaluated
  against corpus-growth degradation (measured at 20% / 50% / 100% of the corpus ingested).

## 7. System-wide known risks

These are elaborated per-module in the relevant LLD docs; listed here because they are
cross-cutting concerns of the whole system.

- **LLM topic-boundary nondeterminism** (duplicate/inconsistent topics) — mitigated via candidate
  shortlisting (cheap embedding/BM25 pre-filter against the existing catalog) before a new topic
  is created. See [LLD/ingestion-agent.md](LLD/ingestion-agent.md).
- **Auto-split correctness under concurrency** — the highest-risk correctness surface in the
  entire engine; requires dedicated concurrent race tests. See [LLD/split.md](LLD/split.md).
- **Graph traversal context blow-up** — bounded by a hard file-count cap of `k + 2k`; the
  benchmark must measure whether traversal ever hurts precision, not just recall. See
  [LLD/query-agent.md](LLD/query-agent.md) and [LLD/graph.md](LLD/graph.md).
- **Benchmark fairness** — the vector-RAG baseline must be well-tuned (real chunk size/overlap,
  reranking if time allows), not a strawman. See [LLD/eval.md](LLD/eval.md).
- **Section-index staleness** — the markdown header-offset cache used for `ReadPartial` must be
  invalidated atomically within the same append/split transaction. See
  [LLD/split.md](LLD/split.md) and [LLD/catalog.md](LLD/catalog.md).
