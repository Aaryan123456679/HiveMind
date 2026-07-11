---
last_synced_commit: 699105baec69c1feff075a58e5ab8d2b054db317
---

# LLD: `agents/llm/`

Status: scaffold only (`agents/llm/__init__.py` empty). See [HLD.md](../HLD.md) for system
context.

## Purpose

Provider-agnostic LLM client interface used by every other agent module, so providers can be
swapped via config without touching agent logic in [`ingestion/`](ingestion-agent.md) or
[`query/`](query-agent.md).

## Interface

A common `LLMClient` protocol/ABC with concrete implementations for:

- **Ollama** (local) — used for ingestion-time segmentation (see
  [ingestion-agent.md](ingestion-agent.md)); default model e.g. Llama 3.1 8B, chosen for cost
  given high call volume.
- **OpenRouter** (GPT-4o-mini) — used for query-time agents.
- **Gemini API** (2.5/3.5 Flash) — alternative for query-time agents.

Selection between providers happens via config, not call-site code changes.

## Design rule (see also [AGENT.md](../../AGENT.md))

No code outside `agents/llm/` may call a provider SDK directly. `agents/ingestion/` and
`agents/query/` only depend on the `LLMClient` interface.

## Interactions with other modules

- `agents/ingestion/` — segmentation agent and `ProposeSplit` route their LLM calls through here
  (Ollama).
- `agents/query/` — intent-refiner, topic-selector, synthesizer all route through here
  (OpenRouter/Gemini).
- `agents/eval/` — the benchmark harness's three retrieval arms share an identical final-answer
  LLM call so only retrieval quality varies; that shared call also goes through this interface, and
  per-call latency/cost interceptors here feed the benchmark's cost/latency metrics.

## Known risks

- None unique to this module; it is the seam other modules' risks (cost, latency) are measured
  through, per [eval.md](eval.md).

## Security note: Gemini API key redaction (subtask 4.5.16.3)

`GeminiClient` (`agents/llm/gemini_client.py`) sends its API key as a `?key=` query parameter
on every request, per Gemini's REST convention — unlike `OllamaClient`/`OpenRouterClient`, which
authenticate via headers (or no auth, for local Ollama). No HTTP logging/tracing/interceptor
layer exists at this seam yet (see the `agents/eval/` per-call latency/cost interceptors above as
the most likely place one would eventually be added), but if/when one is built it **must redact
the full query string for Gemini requests specifically**, not merely an `Authorization` header —
redacting only headers would still leak the Gemini API key in any logged/traced request URL, log
line, or span attribute. See the corresponding docstring note in `agents/llm/gemini_client.py`.

## Cross-references

- [HLD.md](../HLD.md)
- [ingestion-agent.md](ingestion-agent.md), [query-agent.md](query-agent.md) — callers
- [eval.md](eval.md) — cost/latency measurement consumer
