---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# HiveMind

HiveMind is a self-organizing, file-based knowledge brain built as a RAG replacement. Instead of
chunking documents into a vector database, an LLM parses each incoming document once at ingestion
time and segregates its content into topic-coherent `.md` files (not vector chunks). Files
auto-split when they grow too large, and topics are linked via a traversable metadata knowledge
graph. At query time, a two-agent pipeline (intent-refiner + graph-aware topic-selector, with a
tunable `k` hyperparameter for the number of topics retrieved) walks that graph instead of doing
embedding similarity search.

This is a portfolio project demonstrating both ML/agentic engineering and distributed-systems /
storage-engine engineering. It is benchmarked against classic vector RAG and a GraphRAG-style
baseline, and ships with a deployed demo plus load-test results.

## Why this exists

- **GraphRAG** builds graphs over entities extracted from chunks, but retrieval still returns
  chunk/summary text — there's no living, self-splitting document store underneath.
- **RAPTOR** does static hierarchical clustering at index-build time; there's no incremental
  reorganization, and its tree nodes are LLM summaries rather than original content.
- **MemGPT / Letta** page hierarchical memory for a single agent's context window — not a shared,
  graph-linked, browsable multi-user corpus.
- **HiveMind's contribution**: the storage engine itself enforces topical coherence and
  consistency (via MVCC and auto-split) under concurrent access, paired with a learned/agentic
  graph-aware retrieval step that replaces embedding similarity — explicitly evaluated against
  corpus-growth degradation (20% / 50% / 100% ingested checkpoints).

See [docs/HLD.md](docs/HLD.md) for the full system design and the novelty discussion in detail.

## Repo layout

```
HiveMind/
  engine/    # Go module: custom on-disk storage engine (catalog, btree, mvcc, split, graph, wal, rpc, loadtest)
  api/       # Go module: HTTP gateway (auth, rate limiting, routes to engine + agents via gRPC)
  agents/    # Python package: ML/agent layer (ingestion, query, llm provider layer, eval harness)
  proto/     # Shared .proto definitions for Go <-> Python gRPC (not yet written)
  ui/        # React dashboard (not yet built)
  data/      # Dataset prep scripts (not yet built)
  deploy/    # Dockerfiles/compose, CI (not yet built)
```

Architecture at a glance:

```
Client/UI (React) -> API Gateway (Go, api/) -> gRPC -> Go Storage Engine (engine/)
                                              -> gRPC -> Python ML/Agent Service (agents/) -> LLM Provider Layer (Ollama | OpenRouter | Gemini)
```

Full module-by-module design lives in [docs/LLD/](docs/LLD/).

## Building the Go modules

The repo root has a `go.work` workspace covering `engine/` and `api/` (Go 1.26.4):

```bash
# from repo root
go build ./...          # builds both engine and api modules via the workspace
go test ./... -race     # engine concurrency code must always be tested with -race
```

Each module can also be built independently from its own directory (`engine/`, `api/`) since each
has its own `go.mod`.

## Setting up the Python agent environment

```bash
cd agents
python3 -m venv .venv
source .venv/bin/activate
pip install -e ".[dev]"
```

This installs the `ingestion`, `query`, `llm`, and `eval` packages (see
`agents/pyproject.toml`) plus dev tooling (`pytest`, `ruff`).

## Docs

- [docs/HLD.md](docs/HLD.md) — high-level system design (the authoritative description of the
  whole system: architecture, data flow, novelty vs. prior art, known risks).
- [docs/LLD/](docs/LLD/) — one low-level design doc per module, matching the actual
  implementation:
  - Engine (Go): [catalog](docs/LLD/catalog.md), [btree](docs/LLD/btree.md),
    [mvcc](docs/LLD/mvcc.md), [split](docs/LLD/split.md), [graph](docs/LLD/graph.md),
    [wal](docs/LLD/wal.md), [rpc](docs/LLD/rpc.md)
  - Agents (Python): [ingestion-agent](docs/LLD/ingestion-agent.md),
    [query-agent](docs/LLD/query-agent.md), [llm-provider](docs/LLD/llm-provider.md)
  - Benchmarking: [eval](docs/LLD/eval.md)
- [AGENT.md](AGENT.md) — AI/agent operating procedures for working in this repo, including the
  CDR (Coding-Docs-Review) workflow.

## Status

Greenfield / early scaffold stage — module directories exist with placeholder files only
(`doc.go` stubs in `engine/`, an empty `main.go` in `api/`, empty `__init__.py` files in
`agents/`). See the build-phases roadmap in [AGENT.md](AGENT.md) for the implementation plan.
