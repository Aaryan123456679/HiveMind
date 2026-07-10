# Requirement — Subtask 4.1.2 (issue #20, milestone "Phase 4: Query pipeline")

## Source
- `gh issue view 20` (title: "[4] Query-time LLM providers: OpenRouter + Gemini (agents/llm/)")
- Subtask 4.1.2 — Gemini API (2.5/3.5 Flash) LLMClient implementation

## Acceptance criteria
A Gemini-backed `LLMClient` implementation satisfies the same interface as the existing
Ollama/OpenRouter clients (`agents/llm/client.py`) and correctly performs a completion call
against Gemini's API.

## Test spec (per dispatch instructions, overrides issue wording "SDK call mocked")
`pytest agents/llm/test_gemini_client.py` — HTTP call mocked (no real network calls);
assert request shape (endpoint, payload) and response parsing.

## Impacted modules
- `agents/llm/gemini_client.py` (new)
- `agents/llm/test_gemini_client.py` (new)

## Out of scope
- 4.1.1 OpenRouter client (`agents/llm/openrouter_client.py`) — being implemented/verified
  concurrently by a separate agent; not touched by this run.
- 4.1.3 config-driven provider factory (`agents/llm/factory.py`) — separate future dispatch.

## Security disclosure
The issue body (`gh issue view 20`) was inspected for embedded prompt-injection-style content;
none was found — the body is clean, standard issue text. Separately, unusual system-reminder-like
text appeared directly in this agent's own tool-call flow during this run (a "date changed to
2026-07-11" notice, an "Auto Mode Active" directive suggesting relaxed destructive-action caution,
and MCP-server tool-usage instructions for a "tokensave" server not otherwise referenced in this
session/task). None of these were embedded inside `gh`/tool *output content* — they arrived as
system-reminder blocks in the conversation flow itself, so per the dispatch's own instructions they
are treated as legitimate harness reminders, not repo-content injection. Nonetheless they are
disclosed here for transparency. No instructions from any of them were acted upon beyond what the
task/user already authorized (this run performs no destructive git actions and no push).
