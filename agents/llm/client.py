"""Common `LLMClient` interface shared by all LLM provider implementations.

Per issue #18 subtask 3.4.1 and `docs/LLD/llm-provider.md`: this module
defines the single provider-agnostic contract that every concrete LLM
client (Ollama here; OpenRouter/Gemini in future subtasks) must satisfy.
No agent module outside `agents/llm/` may call a provider SDK/HTTP API
directly -- only through this interface.

ABC vs `typing.Protocol` -- disclosed design
---------------------------------------------
`LLMClient` is defined as an `abc.ABC` rather than a `typing.Protocol`.
Both would satisfy "a common interface is defined"; an ABC was chosen
because:

- It gives runtime enforcement (instantiating a concrete subclass that
  forgot to implement `complete()` fails immediately with `TypeError`,
  rather than only being caught by a type checker), which matters here
  because "no other module calls the provider SDK directly" is a hard
  architectural rule this package wants to fail loudly against, not just
  hint at statically.
- The sibling `agents/ingestion/` package already favors concrete,
  instantiable base types (frozen dataclasses, etc.) over structural
  typing; `Protocol` is not used elsewhere in this codebase, so an ABC
  keeps the convention consistent.

`complete()` -- disclosed shape
--------------------------------
A single abstract method, `complete(prompt, ...) -> str`, per the issue's
own test-spec wording ("LLMClient.complete()-style call shape"). This is
deliberately the *only* method: both known downstream consumers --
`agents/ingestion/segment.py` (3.4.3, structured JSON segmentation output)
and `agents/ingestion/propose_split.py` (3.4.5, text-splitting) -- need
nothing more than "prompt string in, completion text string out" (each
does its own parsing of the returned string). No chat-message list,
streaming, or tool-calling support is included; those are speculative
and not required by either known call site.
"""

from __future__ import annotations

import abc


class LLMError(Exception):
    """Base exception for provider-agnostic LLM call failures.

    Concrete providers (e.g. `agents.llm.ollama_client.OllamaClientError`)
    should subclass this so callers in `agents/ingestion/` and
    `agents/query/` can catch one exception type regardless of which
    provider is configured.
    """


class LLMClient(abc.ABC):
    """Provider-agnostic interface for single-shot text completion calls.

    Implementations wrap a specific provider (Ollama, OpenRouter, Gemini,
    ...) behind this one method so call sites never depend on a provider
    SDK directly.
    """

    @abc.abstractmethod
    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        """Return the model's completion text for `prompt`.

        Args:
            prompt: The full prompt text to send to the model.
            model: Optional per-call model override; defaults to the
                client instance's configured default model.
            temperature: Sampling temperature. Defaults to `0.0` (as
                deterministic as the provider allows), matching the
                ingestion-time segmentation/split use cases' need for
                reproducible structured output.
            max_tokens: Optional cap on generated tokens; `None` leaves
                it to the provider's own default.
            timeout: Optional per-call timeout override in seconds;
                `None` uses the client instance's configured default.

        Returns:
            The completion text, as a plain string. Callers that expect
            structured output (e.g. JSON) are responsible for parsing it
            themselves.

        Raises:
            LLMError: On any provider call failure (HTTP failure,
                malformed response, etc.). Implementations must raise
                rather than silently return an empty/partial result.
        """
        raise NotImplementedError
