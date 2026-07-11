"""Test spec for `data/gen_synthetic_pdfs.py` (issue #26, subtask 5.1.2).

Per this subtask's acceptance criteria: verify N PDFs are generated, each references its
seeded topic and at least one cross-topic reference, and PDFs are valid/parseable.

Mocked-LLM tests below (always run, no live services) mirror
`agents/ingestion/test_segment.py`'s `_FakeLLMClient` pattern. An optional, skippable
live-Ollama smoke test at the bottom mirrors `agents/ingestion/test_segment_live.py`'s
established convention: skipped automatically unless a real local Ollama server is
reachable, never required for normal `pytest`/CI runs.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import fitz
import httpx
import pytest


def _extract_normalized_text(pdf_path: Path) -> str:
    """Extract all page text from `pdf_path`, collapsing whitespace/newlines to single
    spaces. PDF text-box word-wrapping can split a multi-word topic title across a line
    break (e.g. "Remote Work Eligibility\\nGuidelines"), which would otherwise defeat a
    naive substring match against the un-wrapped title string -- this normalization makes
    keyword-presence assertions robust to that expected rendering behavior."""
    doc = fitz.open(str(pdf_path))
    text = " ".join(page.get_text() for page in doc)
    doc.close()
    return " ".join(text.split()).lower()

sys.path.insert(0, str(Path(__file__).resolve().parent))

from gen_synthetic_pdfs import (  # noqa: E402
    DEFAULT_TOPICS_PATH,
    Topic,
    TopicManifestError,
    build_doc_specs,
    build_prompt,
    generate_corpus,
    load_topics,
)

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "agents"))

from llm.client import LLMClient, LLMError  # noqa: E402


class _FakeLLMClient(LLMClient):
    """Deterministic `LLMClient` stand-in; returns a canned string naming both the primary
    topic and every cross-referenced topic in its `complete()` output, so downstream
    keyword-presence assertions on generated PDF text are meaningful without a live model.
    """

    def __init__(self) -> None:
        self.calls: list[dict] = []

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        self.calls.append({"prompt": prompt, "model": model, "temperature": temperature})
        # Echo back every quoted topic title found in the prompt, so the fake's output is a
        # faithful (if trivial) stand-in for "an LLM that follows the prompt's instructions
        # to reference every named topic."
        import re

        titles = re.findall(r'"([^"]+)"', prompt)
        body = "This policy covers the following in detail.\n\n"
        for title in titles:
            body += f"As described under the {title!r} policy, related considerations apply here.\n"
        return body


@pytest.fixture()
def fake_llm() -> _FakeLLMClient:
    return _FakeLLMClient()


# --- load_topics ------------------------------------------------------------------------


def test_load_topics_parses_shipped_manifest() -> None:
    topics = load_topics(DEFAULT_TOPICS_PATH)
    assert len(topics) >= 2
    for topic_id, topic in topics.items():
        assert topic.id == topic_id
        assert topic.title
        assert isinstance(topic.key_facts, list)


def test_load_topics_missing_file_raises(tmp_path: Path) -> None:
    with pytest.raises(FileNotFoundError):
        load_topics(tmp_path / "does_not_exist.yaml")


def test_load_topics_malformed_yaml_raises(tmp_path: Path) -> None:
    bad = tmp_path / "bad.yaml"
    bad.write_text("not_topics_key: []\n", encoding="utf-8")
    with pytest.raises(TopicManifestError, match="top-level 'topics'"):
        load_topics(bad)


def test_load_topics_missing_required_field_raises(tmp_path: Path) -> None:
    bad = tmp_path / "bad.yaml"
    bad.write_text("topics:\n  - id: only-an-id\n", encoding="utf-8")
    with pytest.raises(TopicManifestError, match="missing field"):
        load_topics(bad)


def test_load_topics_duplicate_id_raises(tmp_path: Path) -> None:
    bad = tmp_path / "bad.yaml"
    bad.write_text(
        "topics:\n"
        "  - id: dup\n    title: One\n"
        "  - id: dup\n    title: Two\n",
        encoding="utf-8",
    )
    with pytest.raises(TopicManifestError, match="duplicate topic id"):
        load_topics(bad)


# --- build_doc_specs ---------------------------------------------------------------------


def _make_topics(n: int) -> dict[str, Topic]:
    return {f"t{i}": Topic(id=f"t{i}", title=f"Topic {i}", key_facts=[f"fact {i}"]) for i in range(n)}


def test_build_doc_specs_one_per_topic() -> None:
    topics = _make_topics(6)
    specs = build_doc_specs(topics, cross_refs_per_doc=2)
    assert len(specs) == 6
    assert {s.primary_topic_id for s in specs} == set(topics.keys())


def test_build_doc_specs_assigns_requested_cross_reference_count() -> None:
    topics = _make_topics(6)
    specs = build_doc_specs(topics, cross_refs_per_doc=2)
    for spec in specs:
        assert len(spec.cross_topic_ids) == 2


def test_build_doc_specs_no_self_reference() -> None:
    topics = _make_topics(6)
    specs = build_doc_specs(topics, cross_refs_per_doc=3)
    for spec in specs:
        assert spec.primary_topic_id not in spec.cross_topic_ids


def test_build_doc_specs_cross_references_are_distinct() -> None:
    topics = _make_topics(6)
    specs = build_doc_specs(topics, cross_refs_per_doc=3)
    for spec in specs:
        assert len(set(spec.cross_topic_ids)) == len(spec.cross_topic_ids)


def test_build_doc_specs_clamps_cross_refs_to_available_topics() -> None:
    topics = _make_topics(2)
    specs = build_doc_specs(topics, cross_refs_per_doc=10)
    for spec in specs:
        assert len(spec.cross_topic_ids) == 1  # only 1 other topic exists


# --- build_prompt ------------------------------------------------------------------------


def test_build_prompt_includes_primary_and_cross_topic_titles() -> None:
    primary = Topic(id="p", title="Primary Policy", key_facts=["fact A"])
    cross = [Topic(id="c1", title="Cross One"), Topic(id="c2", title="Cross Two")]
    prompt = build_prompt(primary, cross)
    assert "Primary Policy" in prompt
    assert "Cross One" in prompt
    assert "Cross Two" in prompt
    assert "fact A" in prompt


# --- generate_corpus (mocked LLM) ----------------------------------------------------------


def test_generate_corpus_produces_expected_pdf_count(tmp_path: Path, fake_llm: _FakeLLMClient) -> None:
    entries = generate_corpus(
        topics_path=DEFAULT_TOPICS_PATH,
        output_dir=tmp_path,
        llm_client=fake_llm,
        limit=3,
    )
    assert len(entries) == 3
    pdfs = sorted(tmp_path.glob("*.pdf"))
    assert len(pdfs) == 3


def test_generate_corpus_pdfs_are_valid_and_parseable(tmp_path: Path, fake_llm: _FakeLLMClient) -> None:
    generate_corpus(topics_path=DEFAULT_TOPICS_PATH, output_dir=tmp_path, llm_client=fake_llm, limit=3)
    for pdf_path in sorted(tmp_path.glob("*.pdf")):
        doc = fitz.open(str(pdf_path))
        assert doc.page_count >= 1
        text = "".join(page.get_text() for page in doc)
        assert len(text.strip()) > 0
        doc.close()


def test_generate_corpus_pdf_text_contains_primary_and_cross_topic(
    tmp_path: Path, fake_llm: _FakeLLMClient
) -> None:
    entries = generate_corpus(
        topics_path=DEFAULT_TOPICS_PATH, output_dir=tmp_path, llm_client=fake_llm, limit=3
    )
    for entry in entries:
        text = _extract_normalized_text(tmp_path / entry.filename)
        assert entry.primary_topic["title"].lower() in text
        assert len(entry.cross_references) >= 1
        assert any(ref["title"].lower() in text for ref in entry.cross_references)


def test_generate_corpus_writes_manifest_json(tmp_path: Path, fake_llm: _FakeLLMClient) -> None:
    entries = generate_corpus(
        topics_path=DEFAULT_TOPICS_PATH, output_dir=tmp_path, llm_client=fake_llm, limit=3
    )
    manifest_path = tmp_path / "manifest.json"
    assert manifest_path.exists()
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    assert manifest["provider"] == "ollama"
    assert len(manifest["documents"]) == 3
    for doc_json, entry in zip(manifest["documents"], entries):
        assert doc_json["doc_id"] == entry.doc_id
        assert doc_json["filename"] == entry.filename
        assert doc_json["primary_topic"] == entry.primary_topic
        assert doc_json["cross_references"] == entry.cross_references


def test_generate_corpus_calls_injected_llm_client(tmp_path: Path, fake_llm: _FakeLLMClient) -> None:
    generate_corpus(topics_path=DEFAULT_TOPICS_PATH, output_dir=tmp_path, llm_client=fake_llm, limit=2)
    assert len(fake_llm.calls) == 2


def test_generate_corpus_propagates_llm_error(tmp_path: Path) -> None:
    class _FailingClient(LLMClient):
        def complete(self, prompt, *, model=None, temperature=0.0, max_tokens=None, timeout=None):
            raise LLMError("provider call failed")

    with pytest.raises(LLMError):
        generate_corpus(
            topics_path=DEFAULT_TOPICS_PATH, output_dir=tmp_path, llm_client=_FailingClient(), limit=1
        )


def test_generate_corpus_default_client_uses_ollama_provider_explicitly(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """No `llm_client` injected -> must construct via `create_llm_client(provider="ollama")`,
    never relying on `LLM_PROVIDER` env var (which this test deliberately sets to something
    else, to prove the module does not read it)."""
    monkeypatch.setenv("LLM_PROVIDER", "openrouter")  # must be ignored entirely

    captured: dict = {}

    def _fake_factory(provider=None, **kwargs):
        captured["provider"] = provider
        return _FakeLLMClient()

    import gen_synthetic_pdfs

    sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "agents"))
    import llm.factory

    monkeypatch.setattr(llm.factory, "create_llm_client", _fake_factory)
    monkeypatch.setattr(gen_synthetic_pdfs, "_ensure_cross_root_imports", lambda: None)

    # generate_corpus does `from llm.factory import create_llm_client` at call time, so
    # patching the module attribute above is picked up.
    gen_synthetic_pdfs.generate_corpus(topics_path=DEFAULT_TOPICS_PATH, output_dir=tmp_path, limit=1)

    assert captured["provider"] == "ollama"


def test_no_openrouter_or_gemini_code_references_in_source() -> None:
    """Static guard: this module must never *use* OpenRouter/Gemini (import their client
    classes, reference their provider constants/env vars, etc.). The module's own docstring
    legitimately *discusses* OpenRouter/Gemini in prose (explaining why they must not be
    used), so this checks for actual code-level usage patterns, not the bare words anywhere
    in the file."""
    source = Path(__file__).resolve().parent.joinpath("gen_synthetic_pdfs.py").read_text(encoding="utf-8")
    forbidden_patterns = [
        "import llm.openrouter_client",
        "import llm.gemini_client",
        "from llm.openrouter_client",
        "from llm.gemini_client",
        "OpenRouterClient",
        "GeminiClient",
        "PROVIDER_OPENROUTER",
        "PROVIDER_GEMINI",
        "OPENROUTER_API_KEY",
        "GEMINI_API_KEY",
    ]
    for pattern in forbidden_patterns:
        assert pattern not in source, f"forbidden OpenRouter/Gemini usage pattern found: {pattern!r}"


# --- optional live-Ollama smoke test -------------------------------------------------------

_OLLAMA_BASE_URL = "http://localhost:11434"


def _ollama_is_reachable(base_url: str = _OLLAMA_BASE_URL) -> bool:
    try:
        response = httpx.get(base_url, timeout=1.0)
        return response.status_code == 200
    except httpx.HTTPError:
        return False


@pytest.mark.skipif(
    not _ollama_is_reachable(),
    reason=(
        f"no Ollama server reachable at {_OLLAMA_BASE_URL} -- this smoke test is optional and "
        "skipped by default; run `ollama serve` (+ `ollama pull llama3.1:8b`) locally to "
        "exercise it"
    ),
)
def test_live_ollama_smoke_generates_referencing_pdfs(tmp_path: Path) -> None:
    entries = generate_corpus(topics_path=DEFAULT_TOPICS_PATH, output_dir=tmp_path, limit=2)
    assert len(entries) == 2
    for entry in entries:
        pdf_path = tmp_path / entry.filename
        doc = fitz.open(str(pdf_path))
        assert doc.page_count >= 1
        doc.close()
        text = _extract_normalized_text(pdf_path)
        assert len(text.strip()) > 0
        assert entry.primary_topic["title"].lower() in text
        assert any(ref["title"].lower() in text for ref in entry.cross_references)
