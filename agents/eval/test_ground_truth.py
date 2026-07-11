"""Tests for `agents/eval/ground_truth.py` (issue #26, subtask 5.1.3).

Per this subtask's test spec (issue #26's own text): "schema validation for the ground-truth
label file to be written alongside the implementation decision." Covers manifest loading/
validation, topic/query derivation correctness, on-disk ground-truth file schema validation
(round-trip write/load fidelity, malformed-input error handling), and referential integrity
against the real, regenerated `manifest.json` produced by `data/gen_synthetic_pdfs.py` for
this subtask's expanded (32-topic) `topics.yaml`.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from eval.ground_truth import (
    DEFAULT_GROUND_TRUTH_PATH,
    DEFAULT_MANIFEST_PATH,
    GroundTruthError,
    QueryLabel,
    RelevantDoc,
    TopicGroundTruth,
    build_ground_truth_dataset,
    derive_query_set,
    derive_topic_ground_truth,
    load_ground_truth,
    load_manifest,
    write_ground_truth,
)


def _sample_manifest() -> dict:
    return {
        "generated_at": "2026-07-11T00:00:00+00:00",
        "provider": "ollama",
        "model": "llama3.2:latest",
        "topics_path": "/tmp/topics.yaml",
        "documents": [
            {
                "doc_id": "doc-a",
                "filename": "doc-a.pdf",
                "primary_topic": {"id": "a", "title": "Topic A"},
                "cross_references": [{"id": "b", "title": "Topic B"}],
            },
            {
                "doc_id": "doc-b",
                "filename": "doc-b.pdf",
                "primary_topic": {"id": "b", "title": "Topic B"},
                "cross_references": [{"id": "a", "title": "Topic A"}],
            },
        ],
    }


# --- load_manifest ------------------------------------------------------------------------


def test_load_manifest_missing_file_raises(tmp_path: Path) -> None:
    with pytest.raises(FileNotFoundError):
        load_manifest(tmp_path / "nope.json")


def test_load_manifest_invalid_json_raises(tmp_path: Path) -> None:
    bad = tmp_path / "manifest.json"
    bad.write_text("{not valid json", encoding="utf-8")
    with pytest.raises(GroundTruthError, match="invalid JSON"):
        load_manifest(bad)


def test_load_manifest_missing_documents_key_raises(tmp_path: Path) -> None:
    bad = tmp_path / "manifest.json"
    bad.write_text(json.dumps({"provider": "ollama"}), encoding="utf-8")
    with pytest.raises(GroundTruthError, match="'documents'"):
        load_manifest(bad)


def test_load_manifest_empty_documents_raises(tmp_path: Path) -> None:
    bad = tmp_path / "manifest.json"
    bad.write_text(json.dumps({"documents": []}), encoding="utf-8")
    with pytest.raises(GroundTruthError, match="non-empty list"):
        load_manifest(bad)


def test_load_manifest_document_missing_doc_id_raises(tmp_path: Path) -> None:
    manifest = _sample_manifest()
    del manifest["documents"][0]["doc_id"]
    bad = tmp_path / "manifest.json"
    bad.write_text(json.dumps(manifest), encoding="utf-8")
    with pytest.raises(GroundTruthError, match="doc_id"):
        load_manifest(bad)


def test_load_manifest_document_missing_primary_topic_raises(tmp_path: Path) -> None:
    manifest = _sample_manifest()
    del manifest["documents"][0]["primary_topic"]
    bad = tmp_path / "manifest.json"
    bad.write_text(json.dumps(manifest), encoding="utf-8")
    with pytest.raises(GroundTruthError, match="primary_topic"):
        load_manifest(bad)


def test_load_manifest_valid_sample_parses(tmp_path: Path) -> None:
    good = tmp_path / "manifest.json"
    good.write_text(json.dumps(_sample_manifest()), encoding="utf-8")
    manifest = load_manifest(good)
    assert len(manifest["documents"]) == 2


# --- derive_topic_ground_truth ------------------------------------------------------------


def test_derive_topic_ground_truth_one_entry_per_topic() -> None:
    topics = derive_topic_ground_truth(_sample_manifest())
    assert {t.topic_id for t in topics} == {"a", "b"}


def test_derive_topic_ground_truth_labels_primary_and_cross_reference() -> None:
    topics = {t.topic_id: t for t in derive_topic_ground_truth(_sample_manifest())}

    topic_a = topics["a"]
    by_doc = {d.doc_id: d.label for d in topic_a.relevant_docs}
    assert by_doc["doc-a"] == "primary"
    assert by_doc["doc-b"] == "cross_reference"

    topic_b = topics["b"]
    by_doc_b = {d.doc_id: d.label for d in topic_b.relevant_docs}
    assert by_doc_b["doc-b"] == "primary"
    assert by_doc_b["doc-a"] == "cross_reference"


def test_derive_topic_ground_truth_every_topic_has_at_least_one_relevant_doc() -> None:
    topics = derive_topic_ground_truth(_sample_manifest())
    for topic in topics:
        assert len(topic.relevant_docs) >= 1


# --- derive_query_set ----------------------------------------------------------------------


def test_derive_query_set_one_query_per_topic() -> None:
    topics = derive_topic_ground_truth(_sample_manifest())
    queries = derive_query_set(topics)
    assert len(queries) == len(topics)
    assert {q.topic_id for q in queries} == {t.topic_id for t in topics}


def test_derive_query_set_query_text_mentions_topic_title() -> None:
    topics = derive_topic_ground_truth(_sample_manifest())
    queries = {q.topic_id: q for q in derive_query_set(topics)}
    assert "Topic A" in queries["a"].query
    assert "Topic B" in queries["b"].query


def test_derive_query_set_carries_topic_relevance_judgments() -> None:
    topics = derive_topic_ground_truth(_sample_manifest())
    queries = {q.topic_id: q for q in derive_query_set(topics)}
    topics_by_id = {t.topic_id: t for t in topics}
    for topic_id, query in queries.items():
        assert query.relevant_docs == topics_by_id[topic_id].relevant_docs


# --- build_ground_truth_dataset (end to end, fixture manifest) -----------------------------


def test_build_ground_truth_dataset_from_fixture_manifest(tmp_path: Path) -> None:
    manifest_path = tmp_path / "manifest.json"
    manifest_path.write_text(json.dumps(_sample_manifest()), encoding="utf-8")

    dataset = build_ground_truth_dataset(manifest_path=manifest_path)

    assert len(dataset.topics) == 2
    assert len(dataset.queries) == 2
    assert dataset.source_manifest == str(manifest_path.resolve())


# --- write_ground_truth / load_ground_truth (schema validation + round trip) ---------------


def test_write_then_load_ground_truth_round_trips(tmp_path: Path) -> None:
    manifest_path = tmp_path / "manifest.json"
    manifest_path.write_text(json.dumps(_sample_manifest()), encoding="utf-8")
    dataset = build_ground_truth_dataset(manifest_path=manifest_path)

    out_path = tmp_path / "ground_truth.json"
    write_ground_truth(dataset, path=out_path)
    assert out_path.exists()

    loaded = load_ground_truth(out_path)
    assert loaded.source_manifest == dataset.source_manifest
    assert loaded.to_json() == dataset.to_json()


def test_load_ground_truth_missing_file_raises(tmp_path: Path) -> None:
    with pytest.raises(FileNotFoundError):
        load_ground_truth(tmp_path / "nope.json")


def test_load_ground_truth_invalid_json_raises(tmp_path: Path) -> None:
    bad = tmp_path / "ground_truth.json"
    bad.write_text("not json at all {{{", encoding="utf-8")
    with pytest.raises(GroundTruthError, match="invalid JSON"):
        load_ground_truth(bad)


def test_load_ground_truth_missing_top_level_field_raises(tmp_path: Path) -> None:
    bad = tmp_path / "ground_truth.json"
    bad.write_text(json.dumps({"topics": [], "queries": []}), encoding="utf-8")
    with pytest.raises(GroundTruthError, match="source_manifest"):
        load_ground_truth(bad)


def test_load_ground_truth_invalid_relevance_label_raises(tmp_path: Path) -> None:
    bad_payload = {
        "source_manifest": "/tmp/manifest.json",
        "topics": [
            {
                "topic_id": "a",
                "title": "Topic A",
                "relevant_docs": [{"doc_id": "doc-a", "label": "not-a-real-label"}],
            }
        ],
        "queries": [],
    }
    bad = tmp_path / "ground_truth.json"
    bad.write_text(json.dumps(bad_payload), encoding="utf-8")
    with pytest.raises(GroundTruthError, match="label"):
        load_ground_truth(bad)


def test_load_ground_truth_missing_relevant_doc_id_raises(tmp_path: Path) -> None:
    bad_payload = {
        "source_manifest": "/tmp/manifest.json",
        "topics": [
            {
                "topic_id": "a",
                "title": "Topic A",
                "relevant_docs": [{"label": "primary"}],
            }
        ],
        "queries": [],
    }
    bad = tmp_path / "ground_truth.json"
    bad.write_text(json.dumps(bad_payload), encoding="utf-8")
    with pytest.raises(GroundTruthError, match="doc_id"):
        load_ground_truth(bad)


def test_relevant_doc_to_json_shape() -> None:
    doc = RelevantDoc(doc_id="doc-x", label="primary")
    assert doc.to_json() == {"doc_id": "doc-x", "label": "primary"}


def test_topic_ground_truth_to_json_shape() -> None:
    topic = TopicGroundTruth(topic_id="t", title="T", relevant_docs=[RelevantDoc("doc-x", "primary")])
    assert topic.to_json() == {
        "topic_id": "t",
        "title": "T",
        "relevant_docs": [{"doc_id": "doc-x", "label": "primary"}],
    }


def test_query_label_to_json_shape() -> None:
    query = QueryLabel(query="What is the policy on T?", topic_id="t", relevant_docs=[])
    assert query.to_json() == {"query": "What is the policy on T?", "topic_id": "t", "relevant_docs": []}


# --- real, regenerated manifest.json / ground_truth.json (subtask 5.1.3's own live artifacts) ---


@pytest.mark.skipif(
    not DEFAULT_MANIFEST_PATH.exists(),
    reason="requires the regenerated data/synthetic_corpus/generated/manifest.json (subtask 5.1.3)",
)
def test_real_manifest_has_32_topics_and_documents() -> None:
    manifest = load_manifest(DEFAULT_MANIFEST_PATH)
    assert len(manifest["documents"]) == 32

    topics = derive_topic_ground_truth(manifest)
    assert len(topics) == 32
    assert len({t.topic_id for t in topics}) == 32  # no duplicate topic ids


@pytest.mark.skipif(
    not DEFAULT_MANIFEST_PATH.exists(),
    reason="requires the regenerated data/synthetic_corpus/generated/manifest.json (subtask 5.1.3)",
)
def test_real_ground_truth_dataset_referential_integrity() -> None:
    """Every relevant_docs doc_id referenced by the derived ground truth must actually exist
    as a document in the source manifest -- the core "schema validation" guarantee: this
    ground truth is never referencing a document that doesn't exist."""
    manifest = load_manifest(DEFAULT_MANIFEST_PATH)
    real_doc_ids = {d["doc_id"] for d in manifest["documents"]}

    dataset = build_ground_truth_dataset(manifest_path=DEFAULT_MANIFEST_PATH)
    assert len(dataset.topics) == 32
    assert len(dataset.queries) == 32

    for topic in dataset.topics:
        assert topic.relevant_docs, f"topic {topic.topic_id!r} has no relevant documents"
        for rel_doc in topic.relevant_docs:
            assert rel_doc.doc_id in real_doc_ids
            assert rel_doc.label in ("primary", "cross_reference")

    for query in dataset.queries:
        assert query.query
        assert query.relevant_docs


@pytest.mark.skipif(
    not DEFAULT_GROUND_TRUTH_PATH.exists(),
    reason="requires the derived data/synthetic_corpus/ground_truth.json (subtask 5.1.3)",
)
def test_real_ground_truth_file_on_disk_loads_and_validates() -> None:
    """Schema-validates the actual on-disk ground-truth label file this subtask ships,
    per issue #26's own test-spec text."""
    dataset = load_ground_truth(DEFAULT_GROUND_TRUTH_PATH)
    assert len(dataset.topics) == 32
    assert len(dataset.queries) == 32
    assert dataset.source_manifest
