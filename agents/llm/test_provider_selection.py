"""Tests for `llm.factory.create_llm_client` and the config-driven provider
selection it implements.

Per issue #20 subtask 4.1.3's test spec: assert config value X yields
client type X for each supported provider, plus a grep-based test asserting
no provider SDK/concrete-client imports outside `agents/llm/` in the actual
call sites (`agents/ingestion/`, `agents/query/`).

No real network calls anywhere in this suite: the ollama/openrouter/gemini
cases below are exercised via `create_llm_client`'s pure dispatch logic
(constructor argument forwarding only), never via an actual `complete()`
call.
"""

from __future__ import annotations

import re
from pathlib import Path

import pytest

from llm.client import LLMClient, LLMError
from llm.factory import (
    PROVIDER_ENV_VAR,
    PROVIDER_GEMINI,
    PROVIDER_OLLAMA,
    PROVIDER_OPENROUTER,
    SUPPORTED_PROVIDERS,
    LLMFactoryError,
    create_llm_client,
)
from llm.gemini_client import GeminiClient
from llm.ollama_client import OllamaClient
from llm.openrouter_client import OpenRouterClient


@pytest.fixture(autouse=True)
def _clean_provider_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """Ensure `LLM_PROVIDER` never leaks in from the real environment."""
    monkeypatch.delenv(PROVIDER_ENV_VAR, raising=False)


# ---------------------------------------------------------------------------
# V1-V3: config value X yields client type X, for each supported provider
# ---------------------------------------------------------------------------


def test_create_llm_client_ollama() -> None:
    client = create_llm_client(PROVIDER_OLLAMA)
    assert isinstance(client, OllamaClient)
    assert isinstance(client, LLMClient)


def test_create_llm_client_openrouter() -> None:
    client = create_llm_client(PROVIDER_OPENROUTER, api_key="test-key")
    assert isinstance(client, OpenRouterClient)
    assert isinstance(client, LLMClient)


def test_create_llm_client_gemini() -> None:
    client = create_llm_client(PROVIDER_GEMINI, api_key="test-key")
    assert isinstance(client, GeminiClient)
    assert isinstance(client, LLMClient)


# ---------------------------------------------------------------------------
# V4: case-insensitive / whitespace-trimmed provider values
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("raw", ["OLLAMA", "  ollama  ", "Ollama"])
def test_create_llm_client_case_insensitive(raw: str) -> None:
    client = create_llm_client(raw)
    assert isinstance(client, OllamaClient)


# ---------------------------------------------------------------------------
# V5-V6: env var fallback + explicit-arg precedence
# ---------------------------------------------------------------------------


def test_create_llm_client_env_var_fallback(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(PROVIDER_ENV_VAR, PROVIDER_OLLAMA)
    client = create_llm_client()
    assert isinstance(client, OllamaClient)


def test_create_llm_client_explicit_arg_overrides_env(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv(PROVIDER_ENV_VAR, PROVIDER_OLLAMA)
    client = create_llm_client(PROVIDER_OPENROUTER, api_key="test-key")
    assert isinstance(client, OpenRouterClient)


# ---------------------------------------------------------------------------
# V7-V8: clear errors for unknown/missing provider
# ---------------------------------------------------------------------------


def test_create_llm_client_unknown_provider_raises() -> None:
    with pytest.raises(LLMFactoryError, match="anthropic-claude"):
        create_llm_client("anthropic-claude")


def test_create_llm_client_missing_provider_raises(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv(PROVIDER_ENV_VAR, raising=False)
    with pytest.raises(LLMFactoryError, match=PROVIDER_ENV_VAR):
        create_llm_client()


def test_create_llm_client_blank_provider_raises() -> None:
    with pytest.raises(LLMFactoryError):
        create_llm_client("   ")


# ---------------------------------------------------------------------------
# V9: **client_kwargs forwarded to the concrete constructor
# ---------------------------------------------------------------------------


def test_create_llm_client_forwards_kwargs_ollama() -> None:
    client = create_llm_client(PROVIDER_OLLAMA, model="llama3.1:70b")
    assert isinstance(client, OllamaClient)
    assert client._model == "llama3.1:70b"  # noqa: SLF001 -- test asserting forwarding


def test_create_llm_client_forwards_kwargs_openrouter() -> None:
    client = create_llm_client(PROVIDER_OPENROUTER, api_key="sk-forwarded")
    assert isinstance(client, OpenRouterClient)
    assert client._api_key == "sk-forwarded"  # noqa: SLF001


def test_create_llm_client_forwards_kwargs_gemini() -> None:
    client = create_llm_client(PROVIDER_GEMINI, api_key="sk-forwarded")
    assert isinstance(client, GeminiClient)
    assert client._api_key == "sk-forwarded"  # noqa: SLF001


def test_create_llm_client_openrouter_without_api_key_raises_underlying_error() -> None:
    """No api_key kwarg and no OPENROUTER_API_KEY env var -> the factory
    propagates the concrete client's own construction-time error unwrapped
    (factory is a dispatch layer, not an error-translation layer)."""
    import os

    key_was_set = "OPENROUTER_API_KEY" in os.environ
    saved = os.environ.pop("OPENROUTER_API_KEY", None)
    try:
        with pytest.raises(LLMError):
            create_llm_client(PROVIDER_OPENROUTER)
    finally:
        if key_was_set and saved is not None:
            os.environ["OPENROUTER_API_KEY"] = saved


# ---------------------------------------------------------------------------
# V10: LLMFactoryError subclasses the shared LLMError family
# ---------------------------------------------------------------------------


def test_llm_factory_error_is_llm_error() -> None:
    assert issubclass(LLMFactoryError, LLMError)


def test_supported_providers_constant_matches_dispatch() -> None:
    assert set(SUPPORTED_PROVIDERS) == {
        PROVIDER_OLLAMA,
        PROVIDER_OPENROUTER,
        PROVIDER_GEMINI,
    }


# ---------------------------------------------------------------------------
# V11: grep-based test -- no call site outside agents/llm/ imports a
# concrete provider client module or a third-party provider SDK directly.
# ---------------------------------------------------------------------------

#: Concrete-client modules that only `agents/llm/` itself (and its own
#: factory) may import. Test files are deliberately excluded from this
#: scan: `agents/ingestion/test_segment_live.py` and
#: `agents/ingestion/test_e2e_smoke.py` pre-date this subtask and
#: deliberately import `OllamaClient` directly for real-network
#: live/e2e smoke coverage -- see architecture-discovery.md's "Design
#: decision -- grep-based test scope" section for the full rationale.
_FORBIDDEN_IMPORT_PATTERNS = (
    r"\bfrom\s+llm\.ollama_client\s+import\b",
    r"\bfrom\s+llm\.openrouter_client\s+import\b",
    r"\bfrom\s+llm\.gemini_client\s+import\b",
    r"\bimport\s+llm\.ollama_client\b",
    r"\bimport\s+llm\.openrouter_client\b",
    r"\bimport\s+llm\.gemini_client\b",
    r"\bimport\s+google\.generativeai\b",
    r"\bfrom\s+google\.generativeai\b",
    r"\bimport\s+openai\b",
    r"\bfrom\s+openai\b",
    r"\bimport\s+anthropic\b",
    r"\bfrom\s+anthropic\b",
)

_FORBIDDEN_RE = re.compile("|".join(_FORBIDDEN_IMPORT_PATTERNS))


def _agents_root() -> Path:
    return Path(__file__).resolve().parent.parent


def _production_python_files(package_dir: Path) -> list[Path]:
    """All `.py` files under `package_dir`, excluding test files (`test_*.py`)
    and cache/venv directories."""
    files: list[Path] = []
    if not package_dir.is_dir():
        return files
    for path in package_dir.rglob("*.py"):
        if path.name.startswith("test_"):
            continue
        if any(part in {"__pycache__", ".venv"} for part in path.parts):
            continue
        files.append(path)
    return files


def test_no_direct_provider_imports_outside_llm_package() -> None:
    agents_root = _agents_root()
    call_site_dirs = [agents_root / "ingestion", agents_root / "query"]

    offenders: list[str] = []
    for package_dir in call_site_dirs:
        for py_file in _production_python_files(package_dir):
            text = py_file.read_text(encoding="utf-8")
            if _FORBIDDEN_RE.search(text):
                offenders.append(str(py_file.relative_to(agents_root)))

    assert offenders == [], (
        "Found direct provider-SDK/concrete-client imports outside "
        f"agents/llm/ in: {offenders!r}. Call sites in agents/ingestion/ "
        "and agents/query/ must depend only on llm.client.LLMClient "
        "(construct instances via llm.factory.create_llm_client)."
    )


def test_grep_scan_actually_covers_ingestion_package() -> None:
    """Sanity check that the grep-based test above isn't vacuous: confirm
    it actually scanned at least one real production file in
    agents/ingestion/ (guards against the scan silently matching zero
    files due to a path typo)."""
    agents_root = _agents_root()
    scanned = _production_python_files(agents_root / "ingestion")
    assert len(scanned) > 0
