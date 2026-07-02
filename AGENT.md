---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# AGENT.md — AI Operating Procedures for HiveMind

This document tells an AI coding agent (or a human ramping up quickly) how to work in this repo:
where things live, how the two runtimes talk to each other, per-language conventions, and how to
drive the CDR documentation/implementation workflow. For *what the system is and why*, see
[docs/HLD.md](docs/HLD.md). For navigation, see [README.md](README.md).

## System shape in one paragraph

HiveMind has two runtimes that never share process memory: a Go storage engine (`engine/`) that
owns all on-disk state (catalog, B+Tree index, MVCC file versions, graph adjacency, WAL), and a
Python ML/agent service (`agents/`) that owns all LLM calls (ingestion segmentation, query-time
intent refinement/topic selection/synthesis, provider abstraction). A separate Go HTTP gateway
(`api/`) is the only thing a client talks to; it fans out to both backends over gRPC.

## Go <-> Python communication

- Transport is **gRPC, not REST**, specifically because both sides attach interceptors that log
  per-call latency and (for the Python side) LLM token cost — this instrumentation feeds the
  benchmark suite in `agents/eval/`.
- Contracts live in `proto/` (shared `.proto` files, not yet written) and are the single source of
  truth for the wire format between `engine/rpc/`, `api/`, and `agents/`.
- Call direction is bidirectional: `api/` calls into both `engine/rpc/` and `agents/` for normal
  ingest/query traffic, and `engine/split/` calls *into* `agents/ingestion/` (`ProposeSplit`) when
  a file crosses its auto-split threshold. Keep this inversion in mind — the engine is a gRPC
  client of the agent service during splits, not only a server.
- Key RPCs exposed by `engine/rpc/`: `PutSegment`, `GetFile`, `ReadPartial`, `Split`,
  `GraphNeighbors`, `SearchCandidates`.
- Key RPC exposed by `agents/ingestion/`: `ProposeSplit(fileContent) -> [{newPath, sectionRanges}, ...] + redirect summary`.

## Where each concern lives

| Concern | Location |
|---|---|
| On-disk metadata catalog (slotted pages) | `engine/catalog/` |
| Topic-path -> fileID index (B+Tree) | `engine/btree/` |
| Per-file versioning / snapshot reads | `engine/mvcc/` |
| Auto-split orchestration | `engine/split/` (highest-risk correctness surface — see risks below) |
| Knowledge graph adjacency + traversal | `engine/graph/` |
| Write-ahead log + crash recovery | `engine/wal/` |
| gRPC server (engine-side) | `engine/rpc/` |
| Concurrency/load-test harness | `engine/loadtest/` |
| HTTP gateway, auth, rate limiting | `api/` |
| Document normalization + LLM segmentation, `ProposeSplit` | `agents/ingestion/` |
| Intent refinement, topic selection, answer synthesis | `agents/query/` |
| Provider-agnostic LLM client (Ollama / OpenRouter / Gemini) | `agents/llm/` |
| Benchmark harness vs. vector-RAG / GraphRAG baselines | `agents/eval/` |
| Shared gRPC contracts | `proto/` |

## Coding conventions

### Go (`engine/`, `api/`)

- Two independent modules joined by the root `go.work` workspace (Go 1.26.4). Build/test from the
  repo root with `go build ./...` / `go test ./... -race`, or independently from each module dir.
- **All concurrency-sensitive tests must run under `-race`.** This is non-negotiable for
  `engine/mvcc/`, `engine/split/`, `engine/btree/`, and `engine/catalog/`.
- Prefer striped/fine-grained locking (see `catalog/`'s ~256 stripe mutexes, `btree/`'s
  latch-crabbing) over a single global lock — the whole point of this module is concurrency
  credibility.
- Any mutation to catalog/index state must go through the WAL (`engine/wal/`) before being applied
  in memory or on disk.
- New package additions follow the existing `doc.go`-per-package convention for top-level package
  documentation.

### Python (`agents/`)

- Single package `hivemind-agents` (see `agents/pyproject.toml`) with four subpackages:
  `ingestion`, `query`, `llm`, `eval`. Python >= 3.11.
- Env setup: `python3 -m venv .venv && source .venv/bin/activate && pip install -e ".[dev]"`.
- All LLM access must go through the `LLMClient` protocol/ABC in `agents/llm/` — never call a
  provider SDK directly from `ingestion/` or `query/` code. This is what lets providers (Ollama /
  OpenRouter / Gemini) be swapped via config without touching agent logic.
- Ingestion-time segmentation uses local Ollama models (cost reasons, high call volume).
  Query-time agents (intent-refiner, topic-selector, synthesizer) use hosted small models
  (GPT-4o-mini via OpenRouter, or Gemini 2.5/3.5 Flash).
- Lint/format with `ruff`; test with `pytest` (see `[tool.pytest.ini_options]` in
  `agents/pyproject.toml`).

## Known risks to keep front-of-mind

- **Auto-split correctness under concurrency** (`engine/split/`) — the highest-risk module in the
  system. Any change here needs a dedicated concurrent race test (many goroutines appending to the
  same file simultaneously; assert no data loss, exactly-one split per threshold crossing, no
  dangling graph edges).
- **LLM topic-boundary nondeterminism** — mitigated by shortlisting candidate topics (cheap
  embedding/BM25 pre-filter) before letting the segmentation LLM create a new topic, to reduce
  duplicate/drifting topic names.
- **Graph traversal context blow-up** — hard-capped at `k + 2k` total files; the benchmark must
  measure whether traversal ever hurts precision, not just recall.
- **Benchmark fairness** — the vector-RAG baseline in `agents/eval/` must be genuinely well-tuned
  (real chunk size/overlap, reranking if time allows), not a strawman.
- **Section-index staleness** — the markdown header-offset cache used for `ReadPartial` must be
  invalidated atomically within the same append/split transaction that changes the file.

See the corresponding LLD docs for per-module detail on each of these.

## Build-phase roadmap (~6-8 weeks)

1. **Storage core (single-threaded)** — slotted-page catalog, B+Tree, `.md` read/write,
   WAL/recovery.
2. **Concurrency + auto-split** — MVCC, striped locks, latch-crabbing, epoch GC, auto-split + race
   tests.
3. **Graph store + ingestion agents** — adjacency/traversal API, Python normalization +
   segmentation wired end-to-end.
4. **Query pipeline** — intent-refiner, topic-selector, retrieval, synthesis; finalize
   provider-agnostic LLM interface.
5. **Benchmark suite** — baselines, ground-truth finalization, full metrics run.
6. **Demo + deployment + load tests** — React dashboard, deploy, Go load tests, README polish.
7. **Buffer/polish** — demo video, write-up, soak-test fixes.

## CDR workflow for future work sessions

This repo uses the CDR (Context-Driven-Recall) agent workflow to keep documentation, plans, and
implementation in sync (`.cdr/` holds run history, indexes, and memory). When starting new work,
use these commands in order:

- `/cdr:plan` — turn a request into a task/subtask breakdown before writing code.
- `/cdr:implement` — execute a planned subtask; reads targeted LLD + touched files before source.
- `/cdr:verify` — check implementation against plan and canonical docs before commit.
- `/cdr:commit` — create the commit + update `.cdr/index` and `.cdr/commits/` records.
- `/cdr:doc` — this documentation agent; `init` bootstraps docs, `sync` regenerates only drifted
  sections based on a drift report built from `.cdr/index/file.jsonl` + recent impact analyses.
- `/cdr:compact` — compact/prune `.cdr/runs` and stale memory once things get noisy.

Every CDR run writes `.cdr/runs/<date>/<NNN>-<agent>/metadata.json` first and updates its
`status`/`last_completed_step` as it goes, so work is resumable and idempotent (same `run_key` =
short-circuit). Canonical docs (`README.md`, `AGENT.md`, `docs/HLD.md`, `docs/LLD/*.md`) are never
left half-written: if a doc sync would introduce drift relative to the implementation, the write
is rolled back and only a drift report is emitted.
