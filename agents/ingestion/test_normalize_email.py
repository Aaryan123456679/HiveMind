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
FIXTURE_4_MULTIPART = TESTDATA_DIR / "enron_sample_4_multipart.txt"
FIXTURE_5_QUOTED_PRINTABLE = TESTDATA_DIR / "enron_sample_5_quoted_printable.txt"
FIXTURE_6_DISPLAY_NAME_FROM = TESTDATA_DIR / "enron_sample_6_display_name_from.txt"
FIXTURE_7_MULTI_CC = TESTDATA_DIR / "enron_sample_7_multi_cc.txt"


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


def test_multipart_body_extracts_plain_text_part() -> None:
    # FIXTURE_4 is multipart/alternative with a text/plain part first and a
    # text/html part second -- get_body(preferencelist=("plain",)) must pick the
    # plain-text part's content, not the HTML part's.
    fields = normalize_email(FIXTURE_4_MULTIPART)
    assert "Here is the draft Q3 forecast" in fields.body
    assert "<html>" not in fields.body
    assert "HTML copy" not in fields.body
    assert fields.sender == "phillip.allen@enron.com"


def test_quoted_printable_body_is_decoded() -> None:
    # FIXTURE_5 declares Content-Transfer-Encoding: quoted-printable, with an
    # encoded em dash (=E2=80=94) and a soft line break (trailing "="). The decoded
    # body must contain the literal em dash and the soft-broken line rejoined, not
    # the raw quoted-printable escapes.
    fields = normalize_email(FIXTURE_5_QUOTED_PRINTABLE)
    assert "—" in fields.body
    assert "=E2=80=94" not in fields.body
    assert "=\n" not in fields.body
    assert "know if anything shifts" in fields.body


def test_display_name_from_extracts_bare_address() -> None:
    # FIXTURE_6's From header is `"Allen, Phillip K." <phillip.allen@enron.com>`
    # -- a quoted display name containing a comma, a case parseaddr must still
    # resolve to the bare address rather than leaking the display name/comma.
    fields = normalize_email(FIXTURE_6_DISPLAY_NAME_FROM)
    assert fields.sender == "phillip.allen@enron.com"
    assert "Allen" not in fields.sender


def test_multi_cc_addresses_does_not_break_parsing() -> None:
    # FIXTURE_7's Cc header has three comma-separated addresses (one with a quoted
    # display name containing a comma). EmailFields does not expose a `cc` field,
    # but parsing must not raise or corrupt any other extracted field.
    fields = normalize_email(FIXTURE_7_MULTI_CC)
    assert isinstance(fields, EmailFields)
    assert fields.sender == "john.lavorato@enron.com"
    assert fields.subject == "Regional split follow-up"
    assert "Looping in Sally, Steven, and Mark" in fields.body
