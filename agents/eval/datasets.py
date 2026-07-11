"""Common dataset-loader interface for `agents/eval/` (issue #26, subtask 5.1.1).

`docs/LLD/eval.md` describes three benchmark retrieval arms (HiveMind, classic vector RAG,
simplified GraphRAG-style) that must all be evaluated "so that only the retrieval step varies
between arms" -- which in turn requires all three arms to be fed an identical corpus. This module
is that single feed point: it wires task-3.5.1's already-shipped, already-verified dataset loaders
(`data/load_bitext.py`, `data/load_enron.py`) behind one small registry/dispatch interface,
:func:`load_dataset`, rather than having each benchmark arm import and call the two loaders
independently at its own call sites.

No new record type, no re-implemented loading logic -- disclosed design
-------------------------------------------------------------------------
Per the launching task's explicit instruction, this module does not reimplement any of
task-3.5.1's dataset-reading/field-mapping logic. `data/load_bitext.py`'s
`load_bitext_as_raw_documents` and `data/load_enron.py`'s `load_enron_documents` already both
yield `ingestion.rawdoc.RawDocument` -- the system's single, already-established,
source-type-agnostic record shape (issue #17, subtask 3.3.4), used identically regardless of
whether a `RawDocument` originated from a ticket, an email, or (once loaded by a future subtask)
a PDF. Reusing it here, rather than inventing a parallel eval-only record type, is what makes
"assert consistent record shape" in this subtask's test spec meaningful: every dataset this
module exposes yields the exact same dataclass shape (`id`, `source_type`, `text`,
`structured_fields`, `timestamp`), so a caller iterating any registered dataset can rely on that
shape without a per-dataset special case.

Cross-root import wiring -- disclosed, reused precedent
----------------------------------------------------------
`data/` is a sibling of `agents/`, not a package inside it, and has no `__init__.py` of its own
(it relies on Python 3's implicit namespace packages). `agents/ingestion/test_e2e_smoke.py`
(task-3.5.2) already established the working pattern for reaching across that boundary: insert
both `agents/`'s and the repo root's absolute paths onto `sys.path` (idempotently), then import
`data.load_bitext` / `data.load_enron` as dotted names. This module replicates that exact
pattern rather than inventing a new one, so `agents/eval/`'s cross-boundary import behavior stays
consistent with the one other place in the repo that already does this.
"""

from __future__ import annotations

import sys
from collections.abc import Callable, Iterator
from pathlib import Path

from ingestion.rawdoc import RawDocument

_REPO_ROOT = Path(__file__).resolve().parents[2]
_AGENTS_DIR = _REPO_ROOT / "agents"


def _ensure_cross_root_imports() -> None:
    """Idempotently put `agents/` and the repo root onto `sys.path`.

    Needed so that `data.load_bitext`/`data.load_enron` (repo-root-relative dotted imports) are
    resolvable regardless of which directory pytest's own rootdir-insertion already added --
    mirrors `agents/ingestion/test_e2e_smoke.py`'s established wiring for this exact boundary.
    """
    for path in (str(_AGENTS_DIR), str(_REPO_ROOT)):
        if path not in sys.path:
            sys.path.insert(0, path)


def _load_bitext(*, limit: int | None = None) -> Iterator[RawDocument]:
    """Adapter delegating straight through to `data.load_bitext.load_bitext_as_raw_documents`.

    No field-mapping logic of its own -- see module docstring.
    """
    _ensure_cross_root_imports()
    from data.load_bitext import load_bitext_as_raw_documents

    yield from load_bitext_as_raw_documents(limit=limit)


def _load_enron(*, limit: int | None = None) -> Iterator[RawDocument]:
    """Adapter delegating straight through to `data.load_enron.load_enron_documents`.

    No field-mapping logic of its own -- see module docstring.
    """
    _ensure_cross_root_imports()
    from data.load_enron import load_enron_documents

    yield from load_enron_documents(limit=limit)


#: Registry backing the common dataset-loader interface. Each value is a zero-required-argument
#: (besides the keyword-only `limit`) callable yielding `RawDocument` records. A future subtask
#: adding the synthetic-PDF dataset (issue #26's third dataset, out of scope here) registers into
#: this same dict without changing `load_dataset`'s signature or any existing entry.
_LOADERS: dict[str, Callable[..., Iterator[RawDocument]]] = {
    "bitext": _load_bitext,
    "enron": _load_enron,
}


def available_datasets() -> tuple[str, ...]:
    """Return the names of datasets currently registered with the common interface."""
    return tuple(_LOADERS)


def load_dataset(name: str, *, limit: int | None = None) -> Iterator[RawDocument]:
    """Load a registered dataset by name, yielding `RawDocument` records.

    This is the single common entry point all three benchmark retrieval arms (see
    `docs/LLD/eval.md`) should use to obtain a dataset, so that all arms see an identical corpus.

    Args:
        name: One of `available_datasets()` (currently `"bitext"`, `"enron"`).
        limit: If given, stop after yielding this many records.

    Yields:
        `ingestion.rawdoc.RawDocument` instances, in the underlying loader's own order.

    Raises:
        ValueError: If `name` is not a registered dataset name.
    """
    try:
        loader = _LOADERS[name]
    except KeyError:
        available = ", ".join(sorted(_LOADERS)) or "(none registered)"
        raise ValueError(
            f"Unknown dataset {name!r}; available datasets: {available}"
        ) from None
    yield from loader(limit=limit)
