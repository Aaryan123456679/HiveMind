"""Email normalizer for raw Enron-format message files.

Parses a single raw Enron-corpus message file into a common set of fields:
``sender``, ``subject``, ``thread``, ``body``. Uses the stdlib :mod:`email` module
(``email.parser`` with ``email.policy.default``) to parse the RFC 2822-ish message --
no third-party dependency is needed, and no bespoke header parser is written here.

Corpus format
-------------
The real Enron email corpus (the public CMU/FERC ``enron_mail`` maildir dump) stores
each email as one raw message file: a block of ``Key: value`` headers, a blank line,
then a plain-text body. Typical headers include ``Message-ID``, ``Date``, ``From``,
``To``, ``Subject``, ``Cc``, ``Bcc``, ``X-From``, ``X-To``, ``X-cc``, ``X-bcc``,
``X-Folder``, ``X-Origin``, ``X-FileName``. Not every message has every header --
``Cc``/``Bcc``/the ``X-*`` metadata headers are frequently absent or empty, and this
normalizer must not raise on their absence.

Thread id -- disclosed judgment call
-------------------------------------
The corpus has **no native, universally-populated thread-id header**. This normalizer
derives ``thread`` using a two-tier strategy, in priority order:

1. ``In-Reply-To`` header, if present, used verbatim -- it already identifies the
   parent message, the standard signal a threading implementation keys off.
2. ``References`` header, if present (and ``In-Reply-To`` absent) -- the **first**
   message-id in the chain (the thread root) is used, since that is more useful for
   grouping an entire conversation than the last id (which just repeats the immediate
   parent).
3. Otherwise (the common case in this corpus, since most Enron messages lack both of
   the above): a synthetic key derived from the **normalized subject** -- lowercased,
   with a leading run of ``Re:``/``Fwd:``/``Fw:`` prefixes (any casing, repeated, with
   arbitrary surrounding whitespace) stripped, and internal whitespace collapsed. This
   is a standard, defensible fallback used by many email-threading/normalization
   implementations when explicit threading headers are unavailable (the same
   "same-normalized-subject == same-conversation" heuristic used by, e.g., many mail
   clients' conversation view). It is an *approximation*, not a guaranteed-unique
   thread id: two unrelated messages that happen to share an exact subject text will
   collide. This tradeoff is accepted here because no better signal exists in the raw
   corpus for the common case, and it is documented so downstream consumers (e.g. the
   future ``RawDocument``/dispatch pipeline, issue 3.3.4) treat it as approximate.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from email import policy
from email.parser import BytesParser
from email.utils import parseaddr
from pathlib import Path

#: Matches a leading run of reply/forward subject prefixes (``Re:``, ``Fwd:``, ``Fw:``,
#: any casing, optionally repeated, e.g. ``"Re: Fwd: Re: hello"``).
_SUBJECT_PREFIX_RE = re.compile(r"^\s*(?:(?:re|fwd?)\s*:\s*)+", re.IGNORECASE)

#: Collapses any run of whitespace to a single space.
_WHITESPACE_RE = re.compile(r"\s+")


@dataclass(frozen=True)
class EmailFields:
    """Normalized fields extracted from a single raw Enron-format email.

    Attributes:
        sender: Decoded sender address (or best-effort decoded ``From`` text if no
            parseable address is present).
        subject: Decoded ``Subject`` header text, verbatim (empty string if absent).
        thread: Derived thread-grouping key. Either a verbatim ``In-Reply-To``/
            ``References`` message-id (when present -- the reliable case) or a
            normalized-subject-derived fallback key (the common case for this corpus;
            see module docstring for the full disclosed derivation and its tradeoffs).
        body: Decoded plain-text message body.
    """

    sender: str
    subject: str
    thread: str
    body: str


def _normalize_subject(subject: str) -> str:
    """Lowercase, strip reply/forward prefixes, and collapse whitespace in a subject.

    Used both to build the subject-fallback thread key and to make subject-based
    thread grouping robust to ``Re:``/``Fwd:``/casing/whitespace variation across
    messages in the same conversation.
    """
    stripped = _SUBJECT_PREFIX_RE.sub("", subject)
    collapsed = _WHITESPACE_RE.sub(" ", stripped).strip()
    return collapsed.lower()


def _extract_thread(msg, subject: str) -> str:
    """Derive the thread key per the tiered strategy documented in the module docstring."""
    in_reply_to = msg.get("In-Reply-To")
    if in_reply_to and in_reply_to.strip():
        return in_reply_to.strip()

    references = msg.get("References")
    if references and references.strip():
        # References is a whitespace-separated chain of message-ids; the first entry
        # is the thread root, which is more useful for grouping than the last
        # (immediate-parent) entry.
        first_ref = references.split()[0]
        return first_ref.strip()

    return f"subject:{_normalize_subject(subject)}"


def _extract_sender(msg) -> str:
    """Decode the ``From`` header into a usable sender string.

    Prefers the bare address component (via :func:`email.utils.parseaddr`) when one is
    present, since that is the stable identifier across a person's messages even when
    the display name varies; falls back to the raw decoded header text otherwise (e.g.
    a malformed or address-less ``From`` value), so a sender is never silently dropped.
    """
    raw_from = msg.get("From", "")
    _display_name, address = parseaddr(raw_from)
    if address:
        return address
    return raw_from.strip()


def normalize_email(path: str | Path) -> EmailFields:
    """Parse a raw Enron-format email file into normalized sender/subject/thread/body fields.

    Args:
        path: Path to a single raw message file (RFC 2822-ish headers, blank line,
            plain-text body -- the format used by the Enron maildir corpus).

    Returns:
        An :class:`EmailFields` with the extracted ``sender``, ``subject``, ``thread``,
        and ``body``.

    Raises:
        OSError: If ``path`` does not exist or cannot be read.
    """
    path = Path(path)
    with path.open("rb") as fh:
        msg = BytesParser(policy=policy.default).parse(fh)

    subject = str(msg.get("Subject", "")).strip()
    sender = _extract_sender(msg)
    thread = _extract_thread(msg, subject)

    body_part = msg.get_body(preferencelist=("plain",))
    if body_part is not None:
        body = body_part.get_content()
    else:
        # No parseable body part (e.g. missing/blank body) -- fall back to the raw
        # payload rather than raising, since a body-less message is still a valid
        # Enron corpus message that must be normalized, not rejected.
        payload = msg.get_payload(decode=True)
        body = payload.decode("utf-8", errors="replace") if payload else ""

    return EmailFields(sender=sender, subject=subject, thread=thread, body=body.strip())
