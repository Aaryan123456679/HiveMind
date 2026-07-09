"""Tests for `agents.ingestion.normalize_email`.

Fixtures under `agents/ingestion/testdata/` are hand-authored raw-text files modeled
directly on real Enron maildir header conventions (Message-ID, Date, From, To,
Subject, Cc, Bcc, X-From, X-To, X-cc, X-bcc, X-Folder, X-Origin, X-FileName). No
programmatic email generation is used here: since Enron-format messages are plain
RFC-2822-ish text (unlike PDF, which is binary), a real parser run against a hand-
authored raw-text fixture is the correct test design.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from ingestion.normalize_email import EmailFields, normalize_email

TESTDATA_DIR = Path(__file__).parent / "testdata"

FIXTURE_1 = TESTDATA_DIR / "enron_sample_1.txt"
FIXTURE_2_REPLY = TESTDATA_DIR / "enron_sample_2_reply.txt"
FIXTURE_3_MINIMAL = TESTDATA_DIR / "enron_sample_3_no_optional_headers.txt"


def test_sender_extracted() -> None:
    fields = normalize_email(FIXTURE_1)
    assert fields.sender == "phillip.allen@enron.com"


def test_subject_extracted() -> None:
    fields = normalize_email(FIXTURE_1)
    assert fields.subject == "Q2 gas storage numbers"


def test_reply_subject_extracted() -> None:
    fields = normalize_email(FIXTURE_2_REPLY)
    assert fields.subject == "RE: Q2 gas storage numbers"


def test_body_extracted() -> None:
    fields = normalize_email(FIXTURE_1)
    assert "Attached are the Q2 gas storage numbers" in fields.body
    assert "Phillip" in fields.body
    # Ensure it's the sender's own message body, not another fixture's content.
    assert "regional split" not in fields.body


def test_thread_uses_in_reply_to_when_present() -> None:
    fields = normalize_email(FIXTURE_2_REPLY)
    assert fields.thread == "<18782506.1075855378110.JavaMail.evans@thyme>"


def test_thread_fallback_from_subject() -> None:
    # FIXTURE_1 has no In-Reply-To/References -> falls back to normalized-subject key.
    fields = normalize_email(FIXTURE_1)
    assert fields.thread == "subject:q2 gas storage numbers"


def test_subject_normalization_groups_variants() -> None:
    # FIXTURE_1's subject "Q2 gas storage numbers" and FIXTURE_3's
    # "re:   Q2 gas storage numbers" (different case, Re: prefix, extra whitespace,
    # and no In-Reply-To/References on either) should normalize to the same thread key.
    fields_1 = normalize_email(FIXTURE_1)
    fields_3 = normalize_email(FIXTURE_3_MINIMAL)
    assert fields_1.thread == fields_3.thread == "subject:q2 gas storage numbers"


def test_minimal_headers_no_optional_fields() -> None:
    # FIXTURE_3 omits Cc/Bcc/X-* headers entirely -- must not raise, core fields
    # still populate correctly.
    fields = normalize_email(FIXTURE_3_MINIMAL)
    assert isinstance(fields, EmailFields)
    assert fields.sender == "sally.beck@enron.com"
    assert fields.subject.strip().lower().endswith("q2 gas storage numbers")
    assert "No changes needed" in fields.body


def test_normalize_email_raises_on_nonexistent_path(tmp_path: Path) -> None:
    missing_path = tmp_path / "does-not-exist.txt"
    with pytest.raises(OSError):
        normalize_email(missing_path)
