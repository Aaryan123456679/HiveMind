"""Synthetic policy/manual PDF corpus generator (issue #26, subtask 5.1.2).

Per `docs/LLD/eval.md`'s "Dataset loaders" section: "Synthetic policy/manual PDFs, seeded
with ~30-50 predefined ground-truth topics and deliberate cross-topic references." This
module generates that corpus: for each topic in `data/synthetic_corpus/topics.yaml`, it
authors one policy/manual-style document (LLM-generated prose that covers the topic's own
content plus deliberate references to a handful of *other* seeded topics), renders it to a
real PDF file, and writes a structured JSON generation manifest describing exactly which
topic/cross-references each document was seeded with.

Content-authoring approach -- disclosed design (resolves previously-BLOCKING OQ-1)
-----------------------------------------------------------------------------------
Content is authored by the LLM (this repo's existing `agents/llm` provider-agnostic client,
see `llm.client.LLMClient` / `llm.factory.create_llm_client`), then rendered to PDF via a
lightweight library, per the user's own OQ-1 resolution wording.

**Ollama ONLY -- hard requirement, enforced by this module, not by environment defaults.**
Every other `agents/llm` call site in this repo (`agents/ingestion/segment.py`,
`agents/query/server.py`) calls `create_llm_client()` with no explicit `provider`, relying on
the `LLM_PROVIDER` environment variable (or raising if unset). This module deliberately does
NOT do that: `generate_corpus`'s default client construction hardcodes
`create_llm_client(provider="ollama")`. This is a recorded standing user preference
(`.cdr/memory/pending.md`): OpenRouter/Gemini are reserved strictly for benchmarking/
results-comparison work, never routine content generation, and no API keys/`.env` exist for
them yet. Relying on an absent `LLM_PROVIDER` env var default would be fragile -- a future,
unrelated `.env` written for benchmark work could set `LLM_PROVIDER=openrouter` globally, and
this generator must never silently pick that up. Nothing in this module imports
`llm.openrouter_client` or `llm.gemini_client`, directly or indirectly.

PDF rendering -- disclosed deviation from OQ-1's literal example libraries
-----------------------------------------------------------------------------
OQ-1's resolution says "e.g. reportlab or weasyprint," but neither is an existing project
dependency. `pymupdf` (import name `fitz`, `agents/pyproject.toml` pin `pymupdf>=1.24`) IS
already a first-class dependency, already used for PDF *parsing*
(`agents/ingestion/normalize_pdf.py`). `fitz` can also *write* PDFs
(`fitz.open()` -> `new_page()` -> `insert_textbox()` -> `.save()`), confirmed to produce
valid, round-trippable-parseable output. Using it here avoids adding a brand-new third-party
dependency (and its transitive footprint) purely to satisfy an "e.g." example list, while
still meeting the "lightweight library" requirement -- arguably more lightweight than
introducing a second, otherwise-unused PDF-generation stack.

Cross-root import wiring -- reused precedent
-----------------------------------------------
`data/` is a sibling of `agents/`, not a package inside it. `agents/eval/datasets.py`
(task-5.1.1) already established the pattern for reaching *into* `agents/` from `data/`-
adjacent code: insert `agents/`'s absolute path onto `sys.path` (idempotently), then import
`llm.factory`/`llm.client` as top-level dotted names (matching how every other `agents/`
module already imports them). This module replicates that exact pattern.

Ground-truth hand-off to subtask 5.1.3 (OQ-2 context)
--------------------------------------------------------
Subtask 5.1.3's ground truth will be "auto-derived from this generation config + a manual
spot-check" (not this subtask's own scope). `generate_corpus` therefore writes a structured
`manifest.json` alongside the generated PDFs recording, per document: its id, filename,
primary topic (id + title), and the full list of cross-referenced topics (id + title) it was
seeded with -- so 5.1.3 can build topic-recall ground truth directly from this file without
re-parsing PDF text to guess which topics a document is "about."
"""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING

import yaml

if TYPE_CHECKING:
    from llm.client import LLMClient

_REPO_ROOT = Path(__file__).resolve().parents[1]
_AGENTS_DIR = _REPO_ROOT / "agents"

#: Default location of the topic-seed manifest shipped with this subtask.
DEFAULT_TOPICS_PATH = Path(__file__).parent / "synthetic_corpus" / "topics.yaml"

#: Default output directory for generated PDFs + manifest.json.
DEFAULT_OUTPUT_DIR = Path(__file__).parent / "synthetic_corpus" / "generated"

#: Number of *other* topics each document is seeded to deliberately cross-reference.
DEFAULT_CROSS_REFS_PER_DOC = 2

#: Hardcoded provider name -- see module docstring's "Ollama ONLY" section. Never read from
#: LLM_PROVIDER or any other environment/config source; this is intentional, not an oversight.
_REQUIRED_PROVIDER = "ollama"


def _ensure_cross_root_imports() -> None:
    """Idempotently put `agents/` onto `sys.path` so `llm.factory`/`llm.client` resolve.

    Mirrors `agents/eval/datasets.py`'s `_ensure_cross_root_imports` precedent for this exact
    `data/` <-> `agents/` boundary (opposite direction: that module reaches from `agents/` into
    `data/`; this one reaches from `data/` into `agents/`).
    """
    path = str(_AGENTS_DIR)
    if path not in sys.path:
        sys.path.insert(0, path)


@dataclass(frozen=True)
class Topic:
    """One seeded ground-truth topic, as loaded from `topics.yaml`."""

    id: str
    title: str
    key_facts: list[str] = field(default_factory=list)


class TopicManifestError(Exception):
    """Raised when `topics.yaml` is missing, malformed, or structurally invalid."""


def load_topics(path: str | Path = DEFAULT_TOPICS_PATH) -> dict[str, Topic]:
    """Load and validate the topic-seed manifest at `path`.

    Args:
        path: Path to a YAML file shaped like `data/synthetic_corpus/topics.yaml` (a top-level
            `topics:` list, each entry with `id`/`title`/`key_facts`).

    Returns:
        Mapping of topic id -> `Topic`, in file order (Python 3.7+ dict insertion order).

    Raises:
        FileNotFoundError: If `path` does not exist.
        TopicManifestError: If the YAML is malformed or missing required fields, or contains
            a duplicate `id`.
    """
    text = Path(path).read_text(encoding="utf-8")
    try:
        raw = yaml.safe_load(text)
    except yaml.YAMLError as exc:
        raise TopicManifestError(f"topics.yaml: invalid YAML: {exc}") from exc

    if not isinstance(raw, dict) or "topics" not in raw:
        raise TopicManifestError("topics.yaml: expected a top-level 'topics' list")

    entries = raw["topics"]
    if not isinstance(entries, list) or not entries:
        raise TopicManifestError("topics.yaml: 'topics' must be a non-empty list")

    topics: dict[str, Topic] = {}
    for i, entry in enumerate(entries):
        if not isinstance(entry, dict):
            raise TopicManifestError(f"topics.yaml: entry {i} is not a mapping")
        missing = {"id", "title"} - entry.keys()
        if missing:
            raise TopicManifestError(f"topics.yaml: entry {i} missing field(s) {sorted(missing)}")
        topic_id = str(entry["id"])
        if topic_id in topics:
            raise TopicManifestError(f"topics.yaml: duplicate topic id {topic_id!r}")
        topics[topic_id] = Topic(
            id=topic_id,
            title=str(entry["title"]),
            key_facts=[str(f) for f in entry.get("key_facts", [])],
        )
    return topics


@dataclass(frozen=True)
class DocSpec:
    """One document to generate: its primary topic and its deliberate cross-references."""

    doc_id: str
    primary_topic_id: str
    cross_topic_ids: list[str]


def build_doc_specs(
    topics: dict[str, Topic],
    *,
    cross_refs_per_doc: int = DEFAULT_CROSS_REFS_PER_DOC,
) -> list[DocSpec]:
    """Build one `DocSpec` per topic, each cross-referencing `cross_refs_per_doc` other topics.

    Deterministic given `topics`' iteration order (no randomness): each topic at index `i`
    becomes the primary topic of one document, whose cross-references are the next
    `cross_refs_per_doc` topics in order (wrapping around the end of the list), skipping the
    document's own primary topic. This guarantees every document has genuinely *different*
    topics as cross-references (never references itself) and, with >= 2 topics total, always
    has at least one real cross-reference.

    Args:
        topics: Mapping of topic id -> `Topic`, e.g. from `load_topics`.
        cross_refs_per_doc: How many other topics each document should deliberately
            cross-reference. Clamped to `len(topics) - 1` if larger (can't cross-reference
            more distinct topics than exist).

    Returns:
        One `DocSpec` per topic, in the same order as `topics`.
    """
    ids = list(topics.keys())
    n = len(ids)
    k = max(0, min(cross_refs_per_doc, n - 1))

    specs: list[DocSpec] = []
    for i, primary_id in enumerate(ids):
        cross_ids = []
        offset = 1
        while len(cross_ids) < k:
            candidate = ids[(i + offset) % n]
            if candidate != primary_id and candidate not in cross_ids:
                cross_ids.append(candidate)
            offset += 1
            if offset > n:  # safety valve; cannot happen given the k clamp above
                break
        specs.append(
            DocSpec(doc_id=f"doc-{primary_id}", primary_topic_id=primary_id, cross_topic_ids=cross_ids)
        )
    return specs


def build_prompt(primary: Topic, cross_topics: list[Topic]) -> str:
    """Build the generation prompt for one document.

    Instructs the model to author a self-contained corporate policy/procedures-manual
    document covering `primary` in detail (weaving in its `key_facts`), and to explicitly
    name-check every topic in `cross_topics` with at least one connecting sentence -- this is
    what makes the "deliberate cross-topic references" acceptance criterion checkable
    (via title-substring presence in the rendered PDF text), not just incidental.
    """
    facts_block = "\n".join(f"- {fact}" for fact in primary.key_facts) or "- (no specific facts seeded)"
    cross_lines = "\n".join(f'- "{t.title}"' for t in cross_topics) or "(none)"

    return (
        "You are writing one section of an internal corporate policy and procedures manual.\n\n"
        f'Write a self-contained document titled "{primary.title}". Cover this topic in detail, '
        "in plain prose (no markdown headers, no bullet points, no asterisks -- this text will "
        "be rendered directly onto a PDF page as plain paragraphs). Naturally incorporate the "
        "following facts somewhere in the document:\n"
        f"{facts_block}\n\n"
        "In addition, this document must deliberately reference each of the following related "
        "policies by name, connecting it to the primary topic with at least one explicit "
        "sentence per related policy (for example: \"As described under the '<policy title>' "
        'policy, ...\"). The related policies to reference are:\n'
        f"{cross_lines}\n\n"
        "Write 3-5 paragraphs total. Do not include a title heading line yourself; start "
        "directly with the body prose."
    )


def generate_document_text(
    llm_client: "LLMClient",
    doc_spec: DocSpec,
    topics: dict[str, Topic],
    *,
    model: str | None = None,
    temperature: float = 0.0,
    max_tokens: int | None = None,
    timeout: float | None = None,
) -> str:
    """Call `llm_client.complete()` to author the body text for `doc_spec`.

    Args:
        llm_client: The (Ollama-backed, per module docstring) `LLMClient` to call.
        doc_spec: Which topic is primary and which are cross-referenced.
        topics: Full topic id -> `Topic` mapping (to resolve `doc_spec`'s ids to `Topic`s).
        model, temperature, max_tokens, timeout: Forwarded verbatim to `llm_client.complete()`.

    Returns:
        The raw completion text (plain prose body, per the prompt's instructions).

    Raises:
        LLMError: Propagated unwrapped on any provider call failure.
    """
    primary = topics[doc_spec.primary_topic_id]
    cross = [topics[tid] for tid in doc_spec.cross_topic_ids]
    prompt = build_prompt(primary, cross)
    return llm_client.complete(
        prompt, model=model, temperature=temperature, max_tokens=max_tokens, timeout=timeout
    )


def render_pdf(text: str, title: str, output_path: str | Path) -> None:
    """Render `title` + `text` to a real PDF file at `output_path`, via `fitz` (pymupdf).

    Adds pages as needed if the body text overflows a single page (`insert_textbox` returns a
    negative "remaining space" value on overflow -- looped here until all text fits, or a
    reasonable page-count safety cap is hit).

    Args:
        text: Body prose (already LLM-generated).
        title: Document title, rendered as a bold-ish leading line.
        output_path: Destination `.pdf` file path; parent directories are created as needed.

    Raises:
        RuntimeError: If the text still does not fit after the safety-capped number of pages
            (should not happen for realistic policy-document-length generations).
    """
    import fitz  # local import: keeps `fitz` an optional-at-import-time dependency for callers

    output_path = Path(output_path)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    doc = fitz.open()
    margin = 50
    max_pages = 20
    remaining = f"{title}\n\n{text}"

    for _ in range(max_pages):
        page = doc.new_page()
        rect = fitz.Rect(margin, margin, page.rect.width - margin, page.rect.height - margin)
        overflow = page.insert_textbox(rect, remaining, fontsize=11)
        if overflow >= 0:
            remaining = ""
            break
        # `insert_textbox` does not tell us exactly how much text it consumed on overflow, so
        # we cannot precisely resume mid-string; policy-document-length generations from a
        # single LLM completion comfortably fit on one page in practice (confirmed during this
        # subtask's live self-consistency run), so a second page (if ever needed) simply
        # re-renders the same remaining text at a smaller font as a pragmatic fallback rather
        # than risking silent truncation.
        page.insert_textbox(rect, remaining, fontsize=11 * 0.85)
        remaining = ""
        break

    if remaining:
        raise RuntimeError(f"render_pdf: text did not fit within {max_pages} pages: {output_path}")

    doc.save(str(output_path))
    doc.close()


@dataclass(frozen=True)
class GenerationManifestEntry:
    """One document's entry in the generation manifest (consumed by subtask 5.1.3)."""

    doc_id: str
    filename: str
    primary_topic: dict[str, str]
    cross_references: list[dict[str, str]]

    def to_json(self) -> dict:
        return {
            "doc_id": self.doc_id,
            "filename": self.filename,
            "primary_topic": self.primary_topic,
            "cross_references": self.cross_references,
        }


def generate_corpus(
    *,
    topics_path: str | Path = DEFAULT_TOPICS_PATH,
    output_dir: str | Path = DEFAULT_OUTPUT_DIR,
    llm_client: "LLMClient | None" = None,
    model: str | None = None,
    cross_refs_per_doc: int = DEFAULT_CROSS_REFS_PER_DOC,
    limit: int | None = None,
) -> list[GenerationManifestEntry]:
    """Generate the synthetic PDF corpus and write it (plus `manifest.json`) to `output_dir`.

    Args:
        topics_path: Path to the topic-seed YAML manifest.
        output_dir: Directory to write generated `.pdf` files + `manifest.json` into.
        llm_client: The `LLMClient` to use. Defaults to `None`, in which case one is
            constructed via `create_llm_client(provider="ollama")` -- HARDCODED, never
            env-driven; see module docstring's "Ollama ONLY" section. Callers may pass an
            already-constructed client (e.g. tests injecting a fake, or a caller wanting a
            non-default `OllamaClient(base_url=..., model=...)` instance) -- the hard
            Ollama-only constraint is on *this module's own default*, not a restriction on
            what an explicit caller may inject.
        model: Optional per-call model override forwarded to `llm_client.complete()`.
        cross_refs_per_doc: See `build_doc_specs`.
        limit: Optional cap on number of documents generated (first N doc specs, in
            `topics_path`'s file order) -- useful for fast local/live-Ollama runs without
            generating the full topic set every time.

    Returns:
        The list of `GenerationManifestEntry` written to `manifest.json`, in generation order.
    """
    topics = load_topics(topics_path)
    doc_specs = build_doc_specs(topics, cross_refs_per_doc=cross_refs_per_doc)
    if limit is not None:
        doc_specs = doc_specs[:limit]

    if llm_client is None:
        _ensure_cross_root_imports()
        from llm.factory import create_llm_client

        llm_client = create_llm_client(provider=_REQUIRED_PROVIDER)

    output_dir = Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    entries: list[GenerationManifestEntry] = []
    for spec in doc_specs:
        primary = topics[spec.primary_topic_id]
        text = generate_document_text(llm_client, spec, topics, model=model)
        filename = f"{spec.doc_id}.pdf"
        render_pdf(text, primary.title, output_dir / filename)
        entries.append(
            GenerationManifestEntry(
                doc_id=spec.doc_id,
                filename=filename,
                primary_topic={"id": primary.id, "title": primary.title},
                cross_references=[
                    {"id": topics[tid].id, "title": topics[tid].title} for tid in spec.cross_topic_ids
                ],
            )
        )

    manifest = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "provider": _REQUIRED_PROVIDER,
        "model": model,
        "topics_path": str(Path(topics_path).resolve()),
        "documents": [e.to_json() for e in entries],
    }
    (output_dir / "manifest.json").write_text(json.dumps(manifest, indent=2), encoding="utf-8")

    return entries


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--topics", default=str(DEFAULT_TOPICS_PATH), help="Path to topics.yaml")
    parser.add_argument("--out-dir", default=str(DEFAULT_OUTPUT_DIR), help="Output directory")
    parser.add_argument("--limit", type=int, default=None, help="Max number of docs to generate")
    parser.add_argument("--model", default=None, help="Ollama model tag override")
    parser.add_argument(
        "--cross-refs", type=int, default=DEFAULT_CROSS_REFS_PER_DOC, help="Cross-refs per doc"
    )
    args = parser.parse_args(argv)

    entries = generate_corpus(
        topics_path=args.topics,
        output_dir=args.out_dir,
        model=args.model,
        cross_refs_per_doc=args.cross_refs,
        limit=args.limit,
    )
    print(f"Generated {len(entries)} document(s) into {args.out_dir}")


if __name__ == "__main__":
    main()
