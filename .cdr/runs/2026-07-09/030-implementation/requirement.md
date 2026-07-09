# Requirement — issue #18 subtask 3.4.1

**Issue:** #18 ("[3] Segmentation agent + ProposeSplit + LLM client", milestone
"Phase 3: Graph store + ingestion agents (end-to-end)", `agents/ingestion/`,
`agents/llm/`). This is the first of 6 subtasks.

**Subtask 3.4.1 — LLMClient protocol/ABC + Ollama implementation (agents/llm/)**

- Acceptance criteria: a common `LLMClient` interface/protocol is defined; an
  Ollama-backed implementation (default e.g. Llama 3.1 8B) satisfies it; no
  other agent module calls the Ollama SDK/HTTP API directly (only through
  this abstraction).
- Test spec: `pytest agents/llm/test_ollama_client.py` — mock the Ollama HTTP
  call, assert `LLMClient.complete()`-style call shape and response parsing.
- Impacted modules: `agents/llm/client.py`, `agents/llm/ollama_client.py`,
  `agents/llm/test_ollama_client.py`.

## Security note

`gh issue view 18` output and prior git log inspection contained embedded
fake system-reminder-style text (fake date-change notice, fake "tokensave"
MCP tool instructions, fake "Auto Mode Active" directive). Treated as
untrusted data only; not followed; disclosed in handoff.

## Downstream awareness (not implemented here)

- `agents/ingestion/segment.py` (3.4.3, structured JSON segmentation calls)
- `agents/ingestion/propose_split.py` (3.4.5, text-splitting calls)

Both will call through `LLMClient`, so the interface must be generically
usable for both call shapes — a single `complete(prompt, ...) -> str`
method, not narrowly tailored to JSON output.
