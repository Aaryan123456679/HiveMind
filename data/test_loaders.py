"""Tests for `data/load_bitext.py` and `data/load_enron.py` (issue #19, subtask 3.5.1).

Per the issue's test spec: run each loader against its small local fixture sample
(`data/fixtures/bitext_sample.json`, `data/fixtures/enron_sample/`) and assert
expected record counts and field presence. No network access is required -- these
tests never call `refresh_sample_via_datasets_server` and only ever read the
committed fixtures.

Requires `agents/` (specifically the `ingestion` package) importable, i.e. these
tests must run with `agents/.venv` (or an equivalent environment with
`hivemind-agents` installed, e.g. `pip install -e agents/[dev]`) active -- see
`agents/pyproject.toml`. `data/load_bitext.py`/`data/load_enron.py` import `ingestion`
lazily inside their `RawDocument`-building functions specifically so that importing
this test module (and the loader modules) never fails before pytest even gets a
chance to report a clear collection error; if `ingestion` is genuinely missing, the
first `load_*_as_raw_documents`/`load_*_documents` call below raises `ModuleNotFoundError`
with a clear message instead of failing silently or misleadingly earlier.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from load_bitext import (
    DEFAULT_SAMPLE_PATH as BITEXT_SAMPLE_PATH,
    bitext_row_to_ticket_json,
    iter_bitext_records,
    load_bitext_as_raw_documents,
    load_bitext_tickets,
)
from load_enron import (
    DEFAULT_SAMPLE_DIR as ENRON_SAMPLE_DIR,
    iter_enron_sample_paths,
    load_enron_documents,
)

# ---------------------------------------------------------------------------
# Bitext
# ---------------------------------------------------------------------------


def test_bitext_fixture_file_exists() -> None:
    assert BITEXT_SAMPLE_PATH.is_file()


def test_iter_bitext_records_count_and_fields() -> None:
    rows = list(iter_bitext_records())
    assert len(rows) == 30
    for row in rows:
        assert set(row.keys()) >= {"flags", "instruction", "category", "intent", "response"}
        assert row["instruction"]
        assert row["response"]


def test_bitext_row_to_ticket_json_shape() -> None:
    row = next(iter_bitext_records())
    ticket = bitext_row_to_ticket_json(row, 0)
    expected_keys = {
        "ticket_id",
        "subject",
        "description",
        "status",
        "priority",
        "category",
        "requester",
        "assignee",
        "created_at",
        "comments",
    }
    assert set(ticket.keys()) == expected_keys
    assert ticket["ticket_id"] == "BTX-000000"
    assert ticket["description"] == row["instruction"]
    assert ticket["status"] == "closed"
    assert len(ticket["comments"]) == 1
    assert ticket["comments"][0]["body"] == row["response"]


def test_bitext_row_to_ticket_json_ids_are_stable_and_unique() -> None:
    rows = list(iter_bitext_records())
    tickets = [bitext_row_to_ticket_json(r, i) for i, r in enumerate(rows)]
    ids = [t["ticket_id"] for t in tickets]
    assert len(ids) == len(set(ids)) == 30
    assert ids == sorted(ids)


def test_load_bitext_tickets_count_and_limit() -> None:
    all_tickets = list(load_bitext_tickets())
    assert len(all_tickets) == 30

    limited = list(load_bitext_tickets(limit=5))
    assert len(limited) == 5
    assert limited == all_tickets[:5]


def test_load_bitext_as_raw_documents_shape() -> None:
    docs = list(load_bitext_as_raw_documents(limit=3))
    assert len(docs) == 3
    for doc in docs:
        assert doc.source_type == "ticket"
        assert doc.id.startswith("BTX-")
        assert isinstance(doc.text, str) and "TICKET_ID:" in doc.text
        assert doc.structured_fields["status"] == "closed"
        assert doc.timestamp is not None


def test_load_bitext_as_raw_documents_full_sample() -> None:
    docs = list(load_bitext_as_raw_documents())
    assert len(docs) == 30
    assert {doc.id for doc in docs} == {f"BTX-{i:06d}" for i in range(30)}


# ---------------------------------------------------------------------------
# Enron
# ---------------------------------------------------------------------------


def test_enron_fixture_dir_exists() -> None:
    assert ENRON_SAMPLE_DIR.is_dir()


def test_iter_enron_sample_paths_count() -> None:
    paths = list(iter_enron_sample_paths())
    assert len(paths) == 3
    assert all(p.suffix == ".txt" for p in paths)


def test_iter_enron_sample_paths_missing_directory_raises(tmp_path: Path) -> None:
    with pytest.raises(FileNotFoundError):
        list(iter_enron_sample_paths(tmp_path / "does-not-exist"))


def test_load_enron_documents_count_and_shape() -> None:
    docs = list(load_enron_documents())
    assert len(docs) == 3
    for doc in docs:
        assert doc.source_type == "email"
        assert doc.id
        assert isinstance(doc.text, str) and doc.text
        assert {"sender", "subject", "thread"} <= doc.structured_fields.keys()
        assert doc.timestamp is not None


def test_load_enron_documents_limit() -> None:
    docs = list(load_enron_documents(limit=1))
    assert len(docs) == 1


def test_load_enron_documents_reply_thread_uses_in_reply_to() -> None:
    docs = {doc.id: doc for doc in load_enron_documents()}
    reply = docs["msg_002_reply"]
    original = docs["msg_001"]
    # The reply's In-Reply-To header names the original message's Message-ID, so
    # normalize_email's thread-derivation tier 1 should key both onto a related value
    # (the reply's thread == the original's Message-ID, per normalize_email.py).
    assert reply.structured_fields["thread"] == "<25987351.1075851234567.JavaMail.evans@thyme>"
    assert original.id == "msg_001"


def test_load_enron_documents_missing_optional_headers_does_not_raise() -> None:
    docs = {doc.id: doc for doc in load_enron_documents()}
    doc = docs["msg_003_no_optional_headers"]
    assert doc.structured_fields["subject"] == "FERC filing deadline"
