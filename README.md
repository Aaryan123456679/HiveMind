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
baseline (live paid-API run, see [Benchmark results](#benchmark-results)), ships with a React
dashboard and a locally-runnable demo stack (Docker Compose or Kubernetes via `kind`), and includes
Go load/concurrency tests for the storage engine (see [Load testing](#load-testing)).

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
  proto/     # Shared .proto definitions for Go <-> Python gRPC
  ui/        # React dashboard (Vite + TypeScript): query, graph, files/admin views + Playwright e2e
  data/      # Dataset prep scripts (Bitext, Enron loaders, synthetic PDF corpus generator)
  deploy/    # Dockerfiles + docker-compose (local demo) + k8s manifests (kind-validated) + OCI k3s provisioning script
  results/   # Live benchmark run outputs (results JSON, chart, HTML report) per run
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

By default all LLM-backed code paths (ingestion, query, eval) point at a local Ollama instance —
no API key needed to run the system end-to-end. Set `LLM_PROVIDER=openrouter` or
`LLM_PROVIDER=gemini` (plus the matching `OPENROUTER_API_KEY`/`GEMINI_API_KEY`) to use a paid
provider instead; see `agents/.env.example`.

## Running the UI dashboard

```bash
cd ui
npm install
npm run dev        # Vite dev server at http://localhost:5173, expects api/ at :8080
npm test           # vitest unit/component tests
npm run test:e2e   # Playwright e2e smoke test (spins up its own dev server)
```

Routes: `/ingest`, `/query`, `/graph`, `/files`, `/admin`. See `ui/src/App.tsx` for the router and
`ui/src/routes/` for each view's wire contract with the `api/` gateway.

## Running the full stack locally

Two ways to bring up all four services (`engine`, `api`, `agents`, `ui`) together — see
[deploy/README.md](deploy/README.md) for details on both:

**Docker Compose** (fastest path to a working demo):

```bash
cd deploy
cp .env.example .env    # optionally set LLM_PROVIDER/OPENROUTER_API_KEY/GEMINI_API_KEY; defaults to Ollama
docker compose up -d
./smoke-test.sh         # curls api /health and the ui root page, then tears down
```

UI at `http://localhost:8081`, API at `http://localhost:8080`.

**Kubernetes** (manifests validated against a local `kind` cluster; OCI Always-Free k3s
provisioning is scripted but unverified against a live account — see
[deploy/k8s/README.md](deploy/k8s/README.md) and [deploy/oci/README.md](deploy/oci/README.md)):

```bash
kind create cluster --name hivemind
# build + kind load each service image, then:
kubectl apply -f deploy/k8s/
```

## Load testing

`engine/loadtest/` has a reusable goroutine + `hdrhistogram` load-generation harness
(`harness.go`) plus three load tests built on it:

```bash
cd engine
go test ./loadtest/... -bench BenchmarkIngestionThroughput -benchmem -run '^$'   # ingestion throughput (LLM calls mocked)
go test ./loadtest/... -run TestQueryLatencyUnderLoad -race                      # query p50/p95/p99 under concurrent writes
go test ./loadtest/... -race -run TestAutoSplitRaceAtScale                      # auto-split race-correctness at 200-goroutine scale
```

All three exercise the real storage engine (only the LLM/segmentation boundary is mocked) and pass
`-race` clean. See `engine/loadtest/*.go` for measured numbers and the reasoning behind each test's
scale/bound choices.

## Benchmark results

`results/run-001/` is a live paid-API benchmark run (OpenRouter, `gpt-4o-mini`) comparing HiveMind
against a plain vector-RAG baseline and a lightweight GraphRAG-style baseline, at 20%/50%/100% of a
32-document corpus (32 queries per checkpoint, LLM-judged answer quality on a 1-5 scale):

| checkpoint | arm | recall | precision | judge score | cost / 1k queries |
|---|---|---|---|---|---|
| 20% | hivemind | 0.062 | 0.188 | 3.59 | $0.089 |
| 20% | vector_rag | 0.219 | 0.131 | 3.72 | $0.094 |
| 20% | graphrag_lite | 0.188 | 0.113 | 3.58 | $0.093 |
| 50% | hivemind | 0.146 | 0.375 | 3.77 | $0.089 |
| 50% | vector_rag | 0.458 | 0.275 | 3.96 | $0.096 |
| 50% | graphrag_lite | 0.198 | 0.119 | 3.64 | $0.092 |
| 100% | hivemind | 0.302 | 0.719 | 4.23 | $0.094 |
| 100% | vector_rag | 0.823 | 0.494 | 4.42 | $0.096 |
| 100% | graphrag_lite | 0.260 | 0.156 | 3.68 | $0.092 |

Full data, an ASCII chart, and an HTML report are in `results/run-001/` (`live_benchmark_results.json`,
`chart.txt`, `report.html`); run metadata (model, cost cap, actual spend of $0.17) is in
`run-metadata.json`.

Honest read of this run: on this small (32-doc) corpus, plain vector RAG currently leads on raw
recall and judge score at every checkpoint. HiveMind's precision improves faster with corpus growth
than either baseline's (0.19 → 0.38 → 0.72), and per-query cost stays flat and comparable across all
three arms — but this is a single run on a small corpus, not a claim of overall superiority. Rerun
with `python -m eval.run_live_benchmark` (see `agents/eval/run_live_benchmark.py`) for a fresh run;
results will vary run to run.

There is no publicly hosted live demo yet — "Running the full stack locally" above is the current
way to try it end-to-end.

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

All 6 build phases in the [AGENT.md](AGENT.md) roadmap are implemented and independently
verified: storage engine (catalog, btree, MVCC, auto-split, graph, WAL, rpc), the Python
ingestion/query agent pipeline with a pluggable LLM provider layer (Ollama/OpenRouter/Gemini), the
eval harness and live benchmark run (`results/run-001/`), the React dashboard, the Docker
Compose/Kubernetes deploy artifacts, and the Go load/concurrency test suite (`engine/loadtest/`).

There is no publicly hosted live demo — see "Running the full stack locally" above for the
supported ways to run the whole system yourself.
