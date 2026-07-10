# Requirement — Subtask 4.1.1

Source: GitHub issue #20 ("[4] Query-time LLM providers: OpenRouter + Gemini
(agents/llm/)"), milestone "Phase 4: Query pipeline", subtask 4.1.1.

Issue body checked for embedded instructions/prompt-injection: none found (clean).

## Acceptance criteria (verbatim from issue)

> 4.1.1 — OpenRouter (GPT-4o-mini) LLMClient implementation
> Acceptance criteria: An OpenRouter-backed LLMClient implementation
> satisfies the same interface as the Ollama client and correctly performs
> a completion call.
> Test spec: pytest agents/llm/test_openrouter_client.py (HTTP call
> mocked): assert request shape and response parsing.
> Impacted modules: agents/llm/openrouter_client.py,
> agents/llm/test_openrouter_client.py

## Scope boundary (explicit)

This dispatch covers **4.1.1 only**. 4.1.2 (Gemini client) and 4.1.3
(config-driven provider factory) are separate subtasks/dispatches and are
NOT implemented here. No `agents/llm/factory.py` and no
`agents/llm/gemini_client.py` are created in this run.

## Interface contract to satisfy

`agents/llm/client.py`'s `LLMClient` ABC (issue #18 / subtask 3.4.1):
single abstract method `complete(prompt, *, model=None, temperature=0.0,
max_tokens=None, timeout=None) -> str`, raising `LLMError` subclasses on
any failure (never silently swallowing).
