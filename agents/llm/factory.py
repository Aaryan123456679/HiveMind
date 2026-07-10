"""`create_llm_client`: config-driven `LLMClient` factory.

Per issue #20 subtask 4.1.3 and `docs/LLD/llm-provider.md`'s "Selection
between providers happens via config, not call-site code changes" design
rule. This is the single entry point call sites in `agents/ingestion/` and
`agents/query/` should use to obtain an `LLMClient` instance: they name a
provider (directly, or via config/env) and get back a concrete client that
satisfies the shared interface, without importing
`OllamaClient`/`OpenRouterClient`/`GeminiClient` themselves.

Config mechanism -- disclosed choice
--------------------------------------
`docs/LLD/llm-provider.md` says selection happens "via config" but does not
name a specific env var or config-file field. The three existing concrete
clients already establish a convention for their *own* per-provider secret
(API key) resolution: an explicit constructor kwarg first, falling back to
a documented environment variable named by a module-level `*_ENV_VAR`
constant (`OpenRouterClient`'s `API_KEY_ENV_VAR = "OPENROUTER_API_KEY"`,
`GeminiClient`'s `API_KEY_ENV_VAR = "GEMINI_API_KEY"`). This module extends
that same convention one level up, for *provider selection* rather than a
secret: `create_llm_client`'s `provider` argument is resolved explicit-arg
first, environment-variable fallback second, via `PROVIDER_ENV_VAR =
"LLM_PROVIDER"`.

Supported values are `"ollama"`, `"openrouter"`, `"gemini"` (module-level
constants `PROVIDER_OLLAMA`/`PROVIDER_OPENROUTER`/`PROVIDER_GEMINI`,
collected in `SUPPORTED_PROVIDERS`), matched case-insensitively with
surrounding whitespace stripped (a config value is plausibly hand-typed by
an operator, e.g. in a shell env-var export or a YAML file, so this is a
small robustness allowance -- not a spec requirement, but cheap and
harmless).

Any additional keyword arguments passed to `create_llm_client` are
forwarded unchanged to the selected concrete client's constructor (e.g.
`api_key=`, `model=`, `transport=`, `base_url=`, `timeout=`), so callers
that need per-instance overrides (tests injecting an `httpx.MockTransport`,
production code overriding a default model) can still do so through the
factory rather than losing that capability.

Error handling -- disclosed design
------------------------------------
A missing or unrecognized provider value raises `LLMFactoryError`
immediately (never silently defaulting to some provider the caller did not
ask for), naming the resolved value and `SUPPORTED_PROVIDERS` in the
message. `LLMFactoryError` subclasses the shared `agents.llm.client.LLMError`
family so callers can catch one exception type across the whole
`agents/llm/` surface (construction-time misconfiguration and later
per-call failures alike), matching each concrete client's own
`<Provider>ClientError(LLMError)` pattern.
"""

from __future__ import annotations

import os

from llm.client import LLMClient, LLMError
from llm.gemini_client import GeminiClient
from llm.ollama_client import OllamaClient
from llm.openrouter_client import OpenRouterClient

#: Environment variable used as a fallback when `provider` is not passed
#: explicitly to `create_llm_client`. See module docstring's "Config
#: mechanism -- disclosed choice" section.
PROVIDER_ENV_VAR = "LLM_PROVIDER"

#: Supported provider name constants (case-insensitive, whitespace-trimmed
#: at resolution time -- see `_normalize_provider`).
PROVIDER_OLLAMA = "ollama"
PROVIDER_OPENROUTER = "openrouter"
PROVIDER_GEMINI = "gemini"

#: All supported provider values, in the order the issue's own three
#: subtasks introduced their concrete clients.
SUPPORTED_PROVIDERS = (PROVIDER_OLLAMA, PROVIDER_OPENROUTER, PROVIDER_GEMINI)

#: Dispatch table from a normalized provider name to its concrete
#: `LLMClient` implementation.
_PROVIDER_CLASSES: dict[str, type[LLMClient]] = {
    PROVIDER_OLLAMA: OllamaClient,
    PROVIDER_OPENROUTER: OpenRouterClient,
    PROVIDER_GEMINI: GeminiClient,
}


class LLMFactoryError(LLMError):
    """Raised when `create_llm_client` cannot resolve a supported provider.

    Covers both a missing provider value (neither `provider=` nor
    `PROVIDER_ENV_VAR` set) and an unrecognized one (a value that does not
    case-insensitively match any entry in `SUPPORTED_PROVIDERS`).
    """


def _normalize_provider(value: str) -> str:
    """Case-insensitive, whitespace-trimmed normalization of a provider
    name, so config values like `" Ollama "` or `"OPENROUTER"` resolve the
    same as their canonical lowercase form."""
    return value.strip().lower()


def create_llm_client(provider: str | None = None, **client_kwargs: object) -> LLMClient:
    """Return an `LLMClient` instance for the requested provider.

    Args:
        provider: One of `SUPPORTED_PROVIDERS` (case-insensitive,
            whitespace-trimmed). Defaults to `None`, in which case the
            `PROVIDER_ENV_VAR` (`LLM_PROVIDER`) environment variable is
            used. Raises `LLMFactoryError` immediately if neither resolves
            to a supported value.
        **client_kwargs: Forwarded unchanged to the selected concrete
            client's constructor (e.g. `api_key=`, `model=`, `base_url=`,
            `timeout=`, `transport=`).

    Returns:
        A concrete `LLMClient` implementation (`OllamaClient`,
        `OpenRouterClient`, or `GeminiClient`) matching the resolved
        provider.

    Raises:
        LLMFactoryError: If the resolved provider value is missing or not
            one of `SUPPORTED_PROVIDERS`.
        LLMError: Whatever the selected concrete client's own constructor
            raises (e.g. `OpenRouterClientError`/`GeminiClientError` if no
            API key can be resolved), propagated unwrapped -- this factory
            is a dispatch layer, not an error-translation layer.
    """
    resolved = provider if provider is not None else os.environ.get(PROVIDER_ENV_VAR)

    if not resolved or not resolved.strip():
        raise LLMFactoryError(
            "No LLM provider configured: pass provider= explicitly or set "
            f"the {PROVIDER_ENV_VAR} environment variable to one of "
            f"{SUPPORTED_PROVIDERS!r}."
        )

    normalized = _normalize_provider(resolved)
    client_class = _PROVIDER_CLASSES.get(normalized)
    if client_class is None:
        raise LLMFactoryError(
            f"Unknown LLM provider {resolved!r}; supported providers are "
            f"{SUPPORTED_PROVIDERS!r}."
        )

    return client_class(**client_kwargs)
