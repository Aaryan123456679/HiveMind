"""Ground-truth topic/query derivation for `agents/eval/` (issue #26, subtask 5.1.3).

Per issue #26's subtask 5.1.3 ("Ground-truth topic/query finalization"), previously BLOCKED
on OQ-2 (ground-truth curation/verification process). The user has now resolved OQ-2 as:

    Auto-derive ground truth from the generation prompts/config (5.1.2's `topics.yaml` +
    `manifest.json`), verified by a manual spot-check of a random subset (5-10 topics)
    against the actual rendered PDFs -- not a full manual review, not zero review.

This module implements the "auto-derive" half of that resolution. The manual-spot-check half
is a one-time verification activity (performed during this subtask's own self-consistency
step, see `.cdr/runs/2026-07-11/151-implementation/self-consistency.json`), not code -- there
is nothing to "implement" for a human spot-check beyond actually doing it.

Disclosed design -- auto-derivation, not hand curation
-------------------------------------------------------
`data/gen_synthetic_pdfs.py`'s `generate_corpus()` already writes a structured
`manifest.json` recording, per generated document: its `doc_id`, `filename`, `primary_topic`
(id + title), and every `cross_references` topic (id + title) it was deliberately seeded to
reference (see that module's own docstring, "Ground-truth hand-off to subtask 5.1.3" section).
That manifest -- together with `data/synthetic_corpus/topics.yaml`, which this subtask expanded
to 32 topics (within the LLD's ~30-50 target, see `docs/HLD.md`) -- IS the complete input this
module needs. No new hand-authored relevance judgments are introduced anywhere in this file:
a document is judged "relevant" to a topic purely because 5.1.2's generator already recorded
that relationship when it *deliberately* seeded that document to cover (as primary) or
reference (as cross-reference) that topic.

Two label kinds are derived, both required by `docs/LLD/eval.md`'s "Ground-truth topic/query
labels attached to dataset [for] recall/precision measurement" and "Topic-level
recall/precision@k" metric:

- **Topic ground truth** (`TopicGroundTruth`): for each topic, which documents are relevant to
  it, and at what strength (`"primary"` -- the document IS about this topic --  vs.
  `"cross_reference"` -- the document mentions this topic in passing while covering another).
  This distinction lets a future benchmark harness compute recall/precision@k either strictly
  (primary-only) or loosely (primary + cross-reference), a genuine and common IR evaluation
  choice, without this module baking in one answer.
- **Query set** (`QueryLabel`): one simple, deterministic, information-seeking query per topic
  (`"What is the policy on {title}?"`), carrying the same relevant-document judgments as its
  topic. Per OQ-2's "auto-derive from generation config" resolution (not "LLM-author queries"),
  query text is a fixed deterministic template over the topic title -- zero LLM calls, zero
  randomness, fully reproducible from `topics.yaml` alone.

Combined-dataset compatibility -- disclosed scope boundary
-------------------------------------------------------------
Issue #26's acceptance criteria describe a query set with recall/precision labels "attached to
the combined dataset (the synthetic PDF corpus... + the Bitext/Enron datasets... common
interface)". This module's `RelevantDoc.doc_id` values are plain, unnamespaced strings (matching
both the synthetic corpus's `doc_id` shape from `manifest.json` and
`ingestion.rawdoc.RawDocument.id` from `agents/eval/datasets.py`'s Bitext/Enron loaders), so the
schema does not preclude a future query's `relevant_docs` including Bitext/Enron record ids
alongside synthetic-PDF ones. This subtask deliberately does NOT populate any Bitext/Enron
relevance judgments itself: 5.1.1's Bitext/Enron datasets are real-world, unlabeled corpora with
no topic-seeding process analogous to the synthetic generator's -- inventing relevance
judgments for them here would be exactly the kind of un-auto-derivable, hand-curated labeling
OQ-2's resolution was written to avoid. This is a disclosed scope boundary, not an oversight;
wiring cross-arm relevance judgments in is left to whichever future subtask actually builds the
combined-corpus benchmark harness.

Cross-root import wiring -- reused precedent
-----------------------------------------------
This module lives inside `agents/eval/` and only ever reads a `manifest.json` file path handed
to it by the caller (or `topics.yaml` via the same path-argument pattern) -- it does not import
anything from `data/` or `agents/llm/`, so none of `datasets.py`'s or
`gen_synthetic_pdfs.py`'s `sys.path` cross-root wiring is needed here.
"""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Literal

#: Default location of the 5.1.2 generation manifest this module derives ground truth from.
DEFAULT_MANIFEST_PATH = (
    Path(__file__).resolve().parents[2] / "data" / "synthetic_corpus" / "generated" / "manifest.json"
)

#: Default output location for the derived ground-truth label file.
DEFAULT_GROUND_TRUTH_PATH = (
    Path(__file__).resolve().parents[2] / "data" / "synthetic_corpus" / "ground_truth.json"
)

#: Relevance-label kinds a document may carry with respect to a topic. See module docstring's
#: "Two label kinds are derived" section.
RelevanceLabel = Literal["primary", "cross_reference"]

_VALID_LABELS = ("primary", "cross_reference")


class GroundTruthError(Exception):
    """Raised when `manifest.json` (or an on-disk ground-truth file) is missing, malformed, or
    structurally invalid. Mirrors `gen_synthetic_pdfs.TopicManifestError`'s style for this
    module's own analogous input-validation boundary."""


@dataclass(frozen=True)
class RelevantDoc:
    """One document judged relevant to a topic or query, and why.

    `doc_id` is a plain, unnamespaced string -- see module docstring's "Combined-dataset
    compatibility" section for why this is deliberate.
    """

    doc_id: str
    label: RelevanceLabel

    def to_json(self) -> dict:
        return {"doc_id": self.doc_id, "label": self.label}


@dataclass(frozen=True)
class TopicGroundTruth:
    """Ground truth for one seeded topic: which documents are relevant, and at what strength."""

    topic_id: str
    title: str
    relevant_docs: list[RelevantDoc] = field(default_factory=list)

    def to_json(self) -> dict:
        return {
            "topic_id": self.topic_id,
            "title": self.title,
            "relevant_docs": [d.to_json() for d in self.relevant_docs],
        }


@dataclass(frozen=True)
class QueryLabel:
    """One auto-derived query, carrying the same relevance judgments as its source topic."""

    query: str
    topic_id: str
    relevant_docs: list[RelevantDoc] = field(default_factory=list)

    def to_json(self) -> dict:
        return {
            "query": self.query,
            "topic_id": self.topic_id,
            "relevant_docs": [d.to_json() for d in self.relevant_docs],
        }


@dataclass(frozen=True)
class GroundTruthDataset:
    """The full derived ground-truth label file: topics + queries, plus manifest provenance."""

    source_manifest: str
    topics: list[TopicGroundTruth]
    queries: list[QueryLabel]

    def to_json(self) -> dict:
        return {
            "source_manifest": self.source_manifest,
            "topics": [t.to_json() for t in self.topics],
            "queries": [q.to_json() for q in self.queries],
        }


def _require_str(mapping: dict, key: str, *, context: str) -> str:
    if key not in mapping:
        raise GroundTruthError(f"{context}: missing required field {key!r}")
    value = mapping[key]
    if not isinstance(value, str) or not value:
        raise GroundTruthError(f"{context}: field {key!r} must be a non-empty string")
    return value


def load_manifest(path: str | Path = DEFAULT_MANIFEST_PATH) -> dict:
    """Load and structurally validate a `manifest.json` written by `generate_corpus()`.

    Args:
        path: Path to a `manifest.json` file shaped as documented in
            `data/gen_synthetic_pdfs.py`'s `GenerationManifestEntry`.

    Returns:
        The parsed manifest dict (raw, not wrapped in a dataclass -- this module only reads
        the `documents` list's `doc_id`/`primary_topic`/`cross_references` fields).

    Raises:
        FileNotFoundError: If `path` does not exist.
        GroundTruthError: If the JSON is malformed or missing required structure.
    """
    path = Path(path)
    text = path.read_text(encoding="utf-8")
    try:
        manifest = json.loads(text)
    except json.JSONDecodeError as exc:
        raise GroundTruthError(f"{path}: invalid JSON: {exc}") from exc

    if not isinstance(manifest, dict) or "documents" not in manifest:
        raise GroundTruthError(f"{path}: expected a top-level 'documents' list")
    documents = manifest["documents"]
    if not isinstance(documents, list) or not documents:
        raise GroundTruthError(f"{path}: 'documents' must be a non-empty list")

    for i, doc in enumerate(documents):
        context = f"{path}: documents[{i}]"
        if not isinstance(doc, dict):
            raise GroundTruthError(f"{context}: not a mapping")
        _require_str(doc, "doc_id", context=context)
        primary = doc.get("primary_topic")
        if not isinstance(primary, dict):
            raise GroundTruthError(f"{context}: 'primary_topic' must be a mapping")
        _require_str(primary, "id", context=f"{context}.primary_topic")
        _require_str(primary, "title", context=f"{context}.primary_topic")
        cross_refs = doc.get("cross_references")
        if not isinstance(cross_refs, list):
            raise GroundTruthError(f"{context}: 'cross_references' must be a list")
        for j, ref in enumerate(cross_refs):
            if not isinstance(ref, dict):
                raise GroundTruthError(f"{context}.cross_references[{j}]: not a mapping")
            _require_str(ref, "id", context=f"{context}.cross_references[{j}]")
            _require_str(ref, "title", context=f"{context}.cross_references[{j}]")

    return manifest


def derive_topic_ground_truth(manifest: dict) -> list[TopicGroundTruth]:
    """Derive per-topic ground truth from a validated manifest dict (see `load_manifest`).

    For every topic id that appears anywhere in the manifest (as any document's primary topic
    or as any document's cross-reference), collects every document relevant to it: `"primary"`
    if that document's `primary_topic` is this topic, `"cross_reference"` if this topic appears
    in that document's `cross_references`. A document that is simultaneously its own primary
    topic's home AND is cross-referenced by a *different* document naturally yields two
    separate `RelevantDoc` entries (one per document, each correctly labeled from that
    document's own perspective) -- there is no ambiguity or collision, since `relevant_docs` is
    keyed by (topic, document) pair, not by topic alone.

    Args:
        manifest: A dict as returned by `load_manifest`.

    Returns:
        One `TopicGroundTruth` per distinct topic id, in first-seen order (primary topics in
        `documents` order, then any cross-reference-only topic ids -- though in practice every
        seeded topic is some document's primary topic, per `gen_synthetic_pdfs.build_doc_specs`).
    """
    topic_titles: dict[str, str] = {}
    relevant_by_topic: dict[str, list[RelevantDoc]] = {}

    for doc in manifest["documents"]:
        doc_id = doc["doc_id"]
        primary = doc["primary_topic"]
        topic_titles.setdefault(primary["id"], primary["title"])
        relevant_by_topic.setdefault(primary["id"], []).append(
            RelevantDoc(doc_id=doc_id, label="primary")
        )
        for ref in doc["cross_references"]:
            topic_titles.setdefault(ref["id"], ref["title"])
            relevant_by_topic.setdefault(ref["id"], []).append(
                RelevantDoc(doc_id=doc_id, label="cross_reference")
            )

    return [
        TopicGroundTruth(topic_id=topic_id, title=topic_titles[topic_id], relevant_docs=docs)
        for topic_id, docs in relevant_by_topic.items()
    ]


def derive_query_set(topics: list[TopicGroundTruth]) -> list[QueryLabel]:
    """Derive one deterministic query per topic, carrying that topic's relevance judgments.

    Query text is the fixed template `"What is the policy on {title}?"` -- see module
    docstring's "auto-derivation, not hand curation" section for why this is a template, not an
    LLM-authored question.

    Args:
        topics: Per-topic ground truth, e.g. from `derive_topic_ground_truth`.

    Returns:
        One `QueryLabel` per input topic, same order.
    """
    return [
        QueryLabel(
            query=f"What is the policy on {topic.title}?",
            topic_id=topic.topic_id,
            relevant_docs=list(topic.relevant_docs),
        )
        for topic in topics
    ]


def build_ground_truth_dataset(
    manifest_path: str | Path = DEFAULT_MANIFEST_PATH,
) -> GroundTruthDataset:
    """Load `manifest_path`, and derive the full topic + query ground-truth dataset from it.

    Args:
        manifest_path: Path to a `manifest.json` written by `generate_corpus()`.

    Returns:
        A `GroundTruthDataset` with one `TopicGroundTruth` and one `QueryLabel` per topic
        referenced in the manifest.

    Raises:
        FileNotFoundError: If `manifest_path` does not exist.
        GroundTruthError: If the manifest is malformed.
    """
    manifest = load_manifest(manifest_path)
    topics = derive_topic_ground_truth(manifest)
    queries = derive_query_set(topics)
    return GroundTruthDataset(source_manifest=str(Path(manifest_path).resolve()), topics=topics, queries=queries)


def write_ground_truth(dataset: GroundTruthDataset, path: str | Path = DEFAULT_GROUND_TRUTH_PATH) -> None:
    """Write `dataset` to `path` as JSON (creating parent directories as needed)."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(dataset.to_json(), indent=2), encoding="utf-8")


def load_ground_truth(path: str | Path = DEFAULT_GROUND_TRUTH_PATH) -> GroundTruthDataset:
    """Load and structurally validate an on-disk ground-truth label file written by
    `write_ground_truth`.

    Args:
        path: Path to a ground-truth JSON file shaped as `GroundTruthDataset.to_json()`.

    Returns:
        The parsed `GroundTruthDataset`.

    Raises:
        FileNotFoundError: If `path` does not exist.
        GroundTruthError: If the JSON is malformed or missing required structure.
    """
    path = Path(path)
    text = path.read_text(encoding="utf-8")
    try:
        raw = json.loads(text)
    except json.JSONDecodeError as exc:
        raise GroundTruthError(f"{path}: invalid JSON: {exc}") from exc

    if not isinstance(raw, dict):
        raise GroundTruthError(f"{path}: expected a top-level JSON object")
    for key in ("source_manifest", "topics", "queries"):
        if key not in raw:
            raise GroundTruthError(f"{path}: missing required top-level field {key!r}")
    if not isinstance(raw["topics"], list):
        raise GroundTruthError(f"{path}: 'topics' must be a list")
    if not isinstance(raw["queries"], list):
        raise GroundTruthError(f"{path}: 'queries' must be a list")

    def _parse_relevant_docs(items: object, *, context: str) -> list[RelevantDoc]:
        if not isinstance(items, list):
            raise GroundTruthError(f"{context}: 'relevant_docs' must be a list")
        parsed = []
        for i, item in enumerate(items):
            if not isinstance(item, dict):
                raise GroundTruthError(f"{context}.relevant_docs[{i}]: not a mapping")
            doc_id = _require_str(item, "doc_id", context=f"{context}.relevant_docs[{i}]")
            label = item.get("label")
            if label not in _VALID_LABELS:
                raise GroundTruthError(
                    f"{context}.relevant_docs[{i}]: 'label' must be one of {_VALID_LABELS}, got {label!r}"
                )
            parsed.append(RelevantDoc(doc_id=doc_id, label=label))
        return parsed

    topics = []
    for i, t in enumerate(raw["topics"]):
        context = f"{path}: topics[{i}]"
        if not isinstance(t, dict):
            raise GroundTruthError(f"{context}: not a mapping")
        topic_id = _require_str(t, "topic_id", context=context)
        title = _require_str(t, "title", context=context)
        topics.append(
            TopicGroundTruth(
                topic_id=topic_id,
                title=title,
                relevant_docs=_parse_relevant_docs(t.get("relevant_docs"), context=context),
            )
        )

    queries = []
    for i, q in enumerate(raw["queries"]):
        context = f"{path}: queries[{i}]"
        if not isinstance(q, dict):
            raise GroundTruthError(f"{context}: not a mapping")
        query_text = _require_str(q, "query", context=context)
        topic_id = _require_str(q, "topic_id", context=context)
        queries.append(
            QueryLabel(
                query=query_text,
                topic_id=topic_id,
                relevant_docs=_parse_relevant_docs(q.get("relevant_docs"), context=context),
            )
        )

    return GroundTruthDataset(source_manifest=raw["source_manifest"], topics=topics, queries=queries)


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--manifest", default=str(DEFAULT_MANIFEST_PATH), help="Path to gen_synthetic_pdfs.py's manifest.json"
    )
    parser.add_argument(
        "--out", default=str(DEFAULT_GROUND_TRUTH_PATH), help="Output path for the derived ground-truth JSON"
    )
    args = parser.parse_args(argv)

    dataset = build_ground_truth_dataset(manifest_path=args.manifest)
    write_ground_truth(dataset, path=args.out)
    print(
        f"Derived ground truth for {len(dataset.topics)} topic(s) / {len(dataset.queries)} "
        f"quer{'y' if len(dataset.queries) == 1 else 'ies'} into {args.out}"
    )


if __name__ == "__main__":
    main()
