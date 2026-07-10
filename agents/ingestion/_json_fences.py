"""Shared helper: strip a markdown code fence wrapping an LLM's raw completion string.

Extracted (subtask 3.4.6, closing forwarded finding F1 --
`.cdr/index/regression.jsonl`) from `ingestion.propose_split`'s own private
`_strip_code_fences`/`_CODE_FENCE_RE`, which `ingestion.segment` did *not* have and
which real Ollama-backed models were observed (see F1) to need: models sometimes wrap
JSON completions in a ```` ```json ... ``` ```` (or plain ```` ``` ... ``` ````) fence
despite the prompt explicitly saying not to. `propose_split.py` already proactively
guarded against this; `segment.py` did not, so a live model returning a fenced
response would be rejected by `SegmentParseError` as "not valid JSON" even though the
enclosed payload is perfectly well-formed.

Rather than re-deriving the same regex/stripping logic twice (once already
duplicated would already be a smell; a *second* independent copy inside `segment.py`
risks the two silently drifting), this module is the one shared home for it, and both
`ingestion.segment` and `ingestion.propose_split` now import `strip_code_fences` from
here instead of each defining/owning it. Behavior is unchanged from
`propose_split.py`'s original private implementation -- this is a pure extraction, not
a rewrite.
"""

from __future__ import annotations

import re

#: Regex matching a single leading/trailing markdown code fence wrapping the entire
#: response, optionally tagged (```json ... ``` or plain ``` ... ```). Mirrors the
#: shape real Ollama-backed models commonly emit despite prompt instructions against
#: it (see module docstring).
_CODE_FENCE_RE = re.compile(
    r"^\s*```(?:[a-zA-Z0-9_+-]*)\s*\n?(?P<body>.*?)\n?```\s*$",
    re.DOTALL,
)


def strip_code_fences(raw: str) -> str:
    """Strip a single leading/trailing markdown code fence wrapping `raw`, if
    present; otherwise return `raw` unchanged.

    See module docstring.
    """
    match = _CODE_FENCE_RE.match(raw)
    if match:
        return match.group("body")
    return raw
