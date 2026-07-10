"""Dataset loader for an Enron email corpus subsample (issue #19, subtask 3.5.1).

Discovers raw, single-message-per-file Enron-format email files under a local
directory and yields them as fully-built `ingestion.rawdoc.RawDocument` records via
`agents.ingestion.dispatch.dispatch_email` (which in turn uses
`agents.ingestion.normalize_email.normalize_email` -- see that module for the exact
expected on-disk message format: RFC-2822-ish `Key: value` headers, a blank line,
then a plain-text body).

Fixture sample is format-faithful, not a literal corpus download -- disclosed judgment call
-----------------------------------------------------------------------------------------------
The real public Enron corpus (the CMU/FERC `enron_mail` maildir dump) is a ~423MB
archive of ~500k individual message files; downloading and committing any slice of it
verbatim is impractical for this repo (size/CI-runtime constraints) and no lightweight
API exists (unlike Bitext's Hugging Face `datasets-server` rows endpoint, used by
`data/load_bitext.py`) for fetching a small, genuinely-raw-headers subsample on
demand -- the Enron-derived datasets discoverable via that same API during this
session all had headers already stripped (body-only), which does not match what
`normalize_email` parses. `data/fixtures/enron_sample/` therefore contains 3
hand-authored files that are format-faithful to the real corpus's exact on-disk shape
(same header set, same maildir-style `Message-ID`/`X-Folder`/`X-Origin`/`X-FileName`
conventions used by `agents/ingestion/testdata/enron_sample_*.txt`, which
`agents/ingestion/normalize_email.py`'s own tests already established and verified
against as a stand-in for the real corpus, per issue #17). Message content is
invented, not sourced from real Enron correspondence. `load_enron_documents` below
places no assumption on *how* a directory's files were obtained, though -- point it
at a real extracted `maildir/` subtree (or any subset of it) and it works unchanged;
see `DEFAULT_SAMPLE_DIR`'s docstring note.
"""

from __future__ import annotations

from pathlib import Path
from typing import Iterator

#: Small, format-faithful (see module docstring) local sample directory, committed for
#: offline/CI use. Point `load_enron_documents`/`iter_enron_sample_paths` at a real
#: extracted Enron `maildir/` subtree instead to load actual corpus messages -- nothing
#: in this module is fixture-specific beyond this default.
DEFAULT_SAMPLE_DIR = Path(__file__).parent / "fixtures" / "enron_sample"


def iter_enron_sample_paths(directory: str | Path = DEFAULT_SAMPLE_DIR) -> Iterator[Path]:
    """Yield paths to raw Enron-format message files under `directory`, sorted by name.

    Every regular file directly under `directory` is treated as one message file (no
    recursive maildir subfolder walk -- the real corpus's per-mailbox subfolder
    structure, e.g. `inbox/`, `sent/`, is a detail callers loading a real corpus subset
    are expected to flatten themselves, e.g. via `Path.rglob` if that structure is
    wanted; this loader only needs "a directory of message files").

    Args:
        directory: Directory containing raw message files.

    Yields:
        Each file's `Path`, in sorted order (deterministic across runs).

    Raises:
        FileNotFoundError: If `directory` does not exist.
    """
    directory = Path(directory)
    if not directory.is_dir():
        raise FileNotFoundError(f"Enron sample directory not found: {directory}")
    yield from sorted(p for p in directory.iterdir() if p.is_file())


def load_enron_documents(
    directory: str | Path = DEFAULT_SAMPLE_DIR, *, limit: int | None = None
):
    """Load raw Enron-format message files and yield built `RawDocument` records.

    Imports `agents.ingestion.dispatch` lazily (only when this function is called),
    so importing `data.load_enron` itself never requires the `agents/` package or its
    virtualenv to be on `sys.path` -- only actually building `RawDocument`s does.

    Args:
        directory: Directory of raw message files, as accepted by
            `iter_enron_sample_paths`.
        limit: If given, stop after yielding this many documents.

    Yields:
        `ingestion.rawdoc.RawDocument` instances with `source_type="email"`, `id` set
        to the source file's stem (e.g. `"msg_001"` for `msg_001.txt`), and
        `timestamp` defaulted to ingestion-time UTC-now by `dispatch_email` (the
        message's own `Date` header, if present, is not parsed/reused here --
        `normalize_email`/`dispatch_email` do not expose it as a field today).
    """
    from ingestion.dispatch import dispatch_email

    for index, path in enumerate(iter_enron_sample_paths(directory)):
        if limit is not None and index >= limit:
            return
        yield dispatch_email(path.stem, path)
