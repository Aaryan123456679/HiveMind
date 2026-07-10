"""Dataset loader for the Bitext customer-support-ticket dataset (issue #19, subtask 3.5.1).

Loads rows from a local sample of the public
`bitext/Bitext-customer-support-llm-chatbot-training-dataset` (Hugging Face; license
`CDLA-Sharing-1.0`; each row is one synthetic customer message + a matching agent
response, with `category`/`intent` metadata -- no ticket-id/status/timestamp fields of
its own) and converts each row into the ticket-record shape that
`agents.ingestion.normalize_ticket` expects, then (optionally) into a full
`ingestion.rawdoc.RawDocument` via `agents.ingestion.dispatch`.

Real dataset, no network required for tests -- disclosed judgment call
-------------------------------------------------------------------------
The issue's acceptance criteria require loaders "fetch/read" the dataset "from
local/downloaded sources" and a test spec that runs against "a small local fixture
subset" (not a live network fetch), so tests are deterministic/offline-safe. This
module ships both: `DEFAULT_SAMPLE_PATH` points at a **real** 30-row sample of the
actual dataset (`data/fixtures/bitext_sample.json`, fetched once via the Hugging Face
`datasets-server` rows API and committed -- see that file's `_source`/`_license`
metadata keys), and `load_bitext_as_raw_documents`/`load_bitext_tickets` accept any
local JSON path with the same `{"rows": [...]}` (or bare list) shape, so a caller with
a larger downloaded sample (e.g. for the 3.5.2 end-to-end smoke run) can point this
loader at it without code changes. No network call is made by this module itself --
fetching a fresh/larger sample is a separate, explicit step (see
`refresh_sample_via_datasets_server` below), not something a test run triggers.

Bitext -> ticket-record field mapping -- disclosed judgment call
--------------------------------------------------------------------
The Bitext schema (`flags`, `instruction`, `category`, `intent`, `response`) has no
native ticket id, requester/assignee identity, status, priority, or timestamp -- none
of that metadata exists in the source dataset (it was built for chatbot
instruction-tuning, not as a ticketing-system export). This loader synthesizes the
missing fields deterministically and discloses each choice explicitly rather than
silently inventing plausible-looking but fake-precise data:

- ``ticket_id``: ``f"BTX-{index:06d}"`` -- stable given a fixed row order, unique per
  loaded sample.
- ``subject``: the row's `instruction` text, truncated to 80 chars (Bitext
  instructions are themselves short, so truncation rarely triggers).
- ``description``: the row's `instruction` text, verbatim, in full.
- ``status``: always ``"closed"`` -- every Bitext row already carries a completed
  agent `response`, so modeling it as an open/pending ticket would misrepresent the
  source data.
- ``priority``: always ``"normal"`` -- Bitext carries no priority signal at all; this
  is an explicit placeholder, not a derived value.
- ``category``: the row's `category` field, verbatim (Bitext's `intent` is also
  preserved, under `structured_fields` via the dispatch layer... note: `intent` is
  intentionally NOT modeled as a `TicketFields` field, since `TicketFields` has no slot
  for it and this loader does not modify `agents/ingestion/normalize_ticket.py`; callers
  needing `intent` should read the raw row via `iter_bitext_records` directly).
- ``requester`` / ``assignee``: synthetic placeholder identifiers
  (``customer-{index}@example.com`` / ``support-bot@example.com``) -- Bitext contains no
  real identity data (by design; it is a synthetic-instruction dataset).
- ``created_at``: a synthetic, deterministic ISO-8601 UTC timestamp derived from
  `index` (see `_synthetic_created_at`) -- Bitext has no timestamp field at all.
- ``comments``: a single comment (`response`) attributed to `support-bot@example.com`.
"""

from __future__ import annotations

import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Iterable, Iterator

#: Real 30-row sample of the actual Bitext dataset, committed for offline/CI use.
#: See the module docstring and this file's own `_source`/`_license` keys for
#: provenance.
DEFAULT_SAMPLE_PATH = Path(__file__).parent / "fixtures" / "bitext_sample.json"

#: Deterministic synthetic-timestamp epoch used by `_synthetic_created_at` -- an
#: arbitrary, fixed reference instant (not the real ingestion time), chosen purely so
#: `created_at` values are stable and monotonically increasing across a loaded sample.
_SYNTHETIC_EPOCH = datetime(2024, 1, 1, tzinfo=timezone.utc)


def _synthetic_created_at(index: int) -> str:
    """Deterministic, monotonically-increasing ISO-8601 UTC timestamp for row `index`.

    Bitext has no timestamp field; this exists purely so `created_at` is populated
    and stable across repeated loads of the same sample, not to model real dataset
    chronology.
    """
    return (_SYNTHETIC_EPOCH + timedelta(minutes=index)).isoformat()


def iter_bitext_records(path: str | Path = DEFAULT_SAMPLE_PATH) -> Iterator[dict[str, Any]]:
    """Yield raw Bitext rows (unmodified) from a local JSON sample file.

    Args:
        path: Path to a JSON file shaped either as ``{"rows": [...]}`` (the shape
            `data/fixtures/bitext_sample.json` and the Hugging Face `datasets-server`
            rows API both use) or a bare JSON list of row dicts. Defaults to the
            committed real sample (`DEFAULT_SAMPLE_PATH`).

    Yields:
        Each row dict, with the original Bitext keys (`flags`, `instruction`,
        `category`, `intent`, `response`) untouched.

    Raises:
        OSError: If `path` does not exist or cannot be read.
        json.JSONDecodeError: If `path`'s contents are not valid JSON.
        KeyError: If `path` is a JSON object but has no ``"rows"`` key.
    """
    parsed = json.loads(Path(path).read_text(encoding="utf-8"))
    rows: Iterable[dict[str, Any]] = parsed["rows"] if isinstance(parsed, dict) else parsed
    yield from rows


def bitext_row_to_ticket_json(row: dict[str, Any], index: int) -> dict[str, Any]:
    """Convert one raw Bitext row into a `normalize_ticket_json`-ready dict.

    See the module docstring for the full disclosed field-mapping rationale.

    Args:
        row: One raw Bitext row, as yielded by `iter_bitext_records`.
        index: The row's position in the loaded sample (0-based); used to derive a
            stable `ticket_id`, synthetic `created_at`, and placeholder `requester`.

    Returns:
        A dict with exactly the keys `agents.ingestion.normalize_ticket.normalize_ticket_json`
        accepts (`ticket_id`, `subject`, `description`, `status`, `priority`,
        `category`, `requester`, `assignee`, `created_at`, `comments`).
    """
    instruction = str(row.get("instruction", ""))
    response = str(row.get("response", ""))
    return {
        "ticket_id": f"BTX-{index:06d}",
        "subject": instruction[:80],
        "description": instruction,
        "status": "closed",
        "priority": "normal",
        "category": str(row.get("category", "")),
        "requester": f"customer-{index}@example.com",
        "assignee": "support-bot@example.com",
        "created_at": _synthetic_created_at(index),
        "comments": [{"author": "support-bot@example.com", "body": response}],
    }


def load_bitext_tickets(
    path: str | Path = DEFAULT_SAMPLE_PATH, *, limit: int | None = None
) -> Iterator[dict[str, Any]]:
    """Load Bitext rows and yield normalizer-ready ticket dicts.

    This is the "RawDocument-ready input for the normalizers" the issue's acceptance
    criteria describe: each yielded dict is valid input to
    `agents.ingestion.normalize_ticket.normalize_ticket_json` (and, via
    `agents.ingestion.dispatch.dispatch_ticket_json`, to building a full
    `RawDocument`) -- see `load_bitext_as_raw_documents` for the latter.

    Args:
        path: Local JSON sample path, as accepted by `iter_bitext_records`.
        limit: If given, stop after yielding this many records.

    Yields:
        Ticket dicts, per `bitext_row_to_ticket_json`.
    """
    for index, row in enumerate(iter_bitext_records(path)):
        if limit is not None and index >= limit:
            return
        yield bitext_row_to_ticket_json(row, index)


def load_bitext_as_raw_documents(
    path: str | Path = DEFAULT_SAMPLE_PATH, *, limit: int | None = None
):
    """Load Bitext rows and yield fully-built `ingestion.rawdoc.RawDocument` records.

    Imports `agents.ingestion.dispatch` lazily (only when this function is called),
    so importing `data.load_bitext` itself never requires the `agents/` package or its
    virtualenv to be on `sys.path` -- only actually building `RawDocument`s does.

    Args:
        path: Local JSON sample path, as accepted by `iter_bitext_records`.
        limit: If given, stop after yielding this many records.

    Yields:
        `ingestion.rawdoc.RawDocument` instances with `source_type="ticket"`, `id` set
        to the synthesized `ticket_id`, and `timestamp` defaulted to ingestion-time
        UTC-now by `dispatch_ticket_json` (the synthetic `created_at` above is carried
        as `structured_fields["created_at"]`, not reused as `RawDocument.timestamp`).
    """
    from ingestion.dispatch import dispatch_ticket_json

    for ticket in load_bitext_tickets(path, limit=limit):
        yield dispatch_ticket_json(ticket["ticket_id"], ticket)


def refresh_sample_via_datasets_server(
    output_path: str | Path = DEFAULT_SAMPLE_PATH,
    *,
    length: int = 30,
    offset: int = 0,
    timeout_seconds: float = 30.0,
) -> Path:
    """Fetch a fresh Bitext sample from Hugging Face's `datasets-server` rows API.

    Not called by anything in this module or by `data/test_loaders.py` -- tests run
    exclusively against the committed `DEFAULT_SAMPLE_PATH` fixture (per the issue's
    test spec: "a small local fixture subset", no network access required in CI).
    This exists purely as an explicit, opt-in refresh/regeneration path for a human
    maintainer (or the 3.5.2 end-to-end smoke run, if it wants a larger real sample)
    to re-run manually.

    Args:
        output_path: Where to write the fetched sample (same `{"rows": [...]}` shape
            `iter_bitext_records` reads).
        length: Number of rows to fetch (Hugging Face's rows API page size).
        offset: Row offset to start from.
        timeout_seconds: HTTP request timeout.

    Returns:
        `output_path`, for convenience.

    Raises:
        Exception: Any network/HTTP error from the underlying `urllib` request
            (deliberately not narrowed -- this is a manual maintenance utility, not a
            code path exercised by tests).
    """
    import urllib.request

    url = (
        "https://datasets-server.huggingface.co/rows"
        "?dataset=bitext%2FBitext-customer-support-llm-chatbot-training-dataset"
        f"&config=default&split=train&offset={offset}&length={length}"
    )
    with urllib.request.urlopen(url, timeout=timeout_seconds) as response:  # noqa: S310
        payload = json.loads(response.read().decode("utf-8"))

    rows = [entry["row"] for entry in payload["rows"]]
    out = {
        "_source": (
            "HuggingFace bitext/Bitext-customer-support-llm-chatbot-training-dataset"
            f" (train split, offset={offset}, length={length})"
        ),
        "_license": "CDLA-Sharing-1.0",
        "_fetched_via": url,
        "rows": rows,
    }
    output_path = Path(output_path)
    output_path.write_text(json.dumps(out, indent=2), encoding="utf-8")
    return output_path
