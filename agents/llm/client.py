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
from dataclasses import dataclass


class LLMError(Exception):
    """Base exception for provider-agnostic LLM call failures.

    Concrete providers (e.g. `agents.llm.ollama_client.OllamaClientError`)
    should subclass this so callers in `agents/ingestion/` and
    `agents/query/` can catch one exception type regardless of which
    provider is configured.
    """


@dataclass(frozen=True)
class TokenUsage:
    """Provider-reported token counts for a single `complete()` call.

    Added for subtask 4.5.19.1 (issue #59): `agents/llm/interceptor.py` needs each provider's
    actual reported prompt/completion token counts to compute `cost_usd` for paid providers.
    Both fields are required (not optional) -- `CompletionResult.usage` itself is the optional
    slot for "this provider didn't report usage", so once a `TokenUsage` exists both counts are
    assumed present and well-typed (the provider client is responsible for that validation
    before constructing one; see `openrouter_client.py`/`gemini_client.py`).
    """

    prompt_tokens: int
    completion_tokens: int


@dataclass(frozen=True)
class CompletionResult:
    """Full result of a `complete()`-style call: completion text plus optional token usage.

    Added for subtask 4.5.19.1 (issue #59) alongside `LLMClient.complete_with_usage()` below.
    `complete()` itself keeps returning a plain `str` (unchanged, backward-compatible with every
    existing caller in `agents/ingestion/`/`agents/query/` and every existing test); this richer
    shape is only surfaced through the new `complete_with_usage()` method, which
    `agents/llm/interceptor.py` calls when it needs cost data.

    Attributes:
        text: The completion text -- identical to what `complete()` itself returns for the same
            call.
        model: The resolved model name actually used for this call (the per-call `model=`
            override if given, else the client instance's configured default). Used by the
            interceptor to look up a per-model rate in its rate table.
        usage: Token counts reported by the provider, if any. `None` when the provider's response
            didn't include (or a client hasn't been extended to parse) usage data -- e.g.
            `OllamaClient`, which never overrides `complete_with_usage()` and so always reports
            `usage=None` here (harmless, since Ollama's cost is always `$0.0` regardless of
            usage).
    """

    text: str
    model: str
    usage: TokenUsage | None = None


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

    def complete_with_usage(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> CompletionResult:
        """Return the model's completion text plus token usage, if available.

        Added for subtask 4.5.19.1 (issue #59): `agents/llm/interceptor.py` calls this (not
        `complete()`) when it needs to compute a paid provider's `cost_usd`. Deliberately a
        **concrete** method with a default implementation, not `abc.abstractmethod` --
        `complete()` remains the only required contract every `LLMClient` implementation must
        satisfy (preserving the existing "instantiating a subclass that forgot `complete()`
        fails immediately with `TypeError`" guarantee this class's docstring describes), while
        providers that can surface real token usage (`OpenRouterClient`, `GeminiClient`) opt in
        by overriding this method instead of being forced to.

        This default implementation simply delegates to `complete()` and reports no usage data
        (`usage=None`) -- correct behavior for any provider that has not been extended to parse
        token counts out of its response (e.g. `OllamaClient`, whose cost is always `$0.0`
        regardless of usage per the established convention, so it has no need to override this).

        Args:
            Same as `complete()`.

        Returns:
            A `CompletionResult` with `text` identical to what `complete()` would return for an
            equivalent call, `model` resolved to whatever this instance would actually use
            (falls back to the private `_model` attribute every concrete client already stores,
            if present; `""` otherwise), and `usage=None`.

        Raises:
            LLMError: Whatever `complete()` itself raises, propagated unchanged.
        """
        text = self.complete(
            prompt,
            model=model,
            temperature=temperature,
            max_tokens=max_tokens,
            timeout=timeout,
        )
        resolved_model = model if model is not None else getattr(self, "_model", "")
        return CompletionResult(text=text, model=resolved_model, usage=None)
