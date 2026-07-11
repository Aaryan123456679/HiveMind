"""Tests for `agents/eval/datasets.py` (issue #26, subtask 5.1.1).

Per the issue's test spec: load both the Bitext and Enron datasets through the common
dataset-loader interface (`load_dataset`) and assert a consistent record shape across both --
not just that each loader individually runs without error.
"""

from __future__ import annotations

import pytest

from eval.datasets import available_datasets, load_dataset
from ingestion.rawdoc import RawDocument


def test_available_datasets_includes_bitext_and_enron() -> None:
    """The common interface must expose both task-3.5.1 datasets by name."""
    names = available_datasets()
    assert "bitext" in names
    assert "enron" in names


@pytest.mark.parametrize("name", ["bitext", "enron"])
def test_load_dataset_yields_consistent_record_shape(name: str) -> None:
    """Both datasets, loaded through the one common entry point, yield the same record shape."""
    records = list(load_dataset(name, limit=5))

    assert records, f"{name!r} loader yielded no records"

    for record in records:
        assert isinstance(record, RawDocument)
        assert isinstance(record.id, str) and record.id
        assert record.source_type in ("pdf", "email", "ticket")
        assert isinstance(record.text, str) and record.text
        assert isinstance(record.structured_fields, dict)
        assert record.timestamp is not None


def test_bitext_and_enron_have_distinct_source_types() -> None:
    """Sanity check that the two datasets are genuinely different, not accidentally aliased."""
    bitext_records = list(load_dataset("bitext", limit=3))
    enron_records = list(load_dataset("enron", limit=3))

    assert {record.source_type for record in bitext_records} == {"ticket"}
    assert {record.source_type for record in enron_records} == {"email"}


def test_load_dataset_respects_limit() -> None:
    """`limit` is forwarded through the common interface to the underlying loader."""
    assert len(list(load_dataset("bitext", limit=2))) == 2
    assert len(list(load_dataset("enron", limit=1))) == 1


def test_unknown_dataset_name_raises_value_error() -> None:
    """An unregistered dataset name is a clear error, not a silent empty iterator."""
    with pytest.raises(ValueError, match="Unknown dataset"):
        list(load_dataset("nope"))
