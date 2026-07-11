"""Shared helpers for cleaning up an LLM's raw JSON completion string before
`json.loads` sees it.

## Markdown code-fence stripping (subtask 3.4.6, closed forwarded finding F1)

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

## Control-character / triple-quote sanitization (issue #44, closing forwarded
## finding F7)

A *distinct* real-model reliability gap from F1, surfaced by issue #19 subtask
3.5.2's real end-to-end smoke run against a live local `llama3.1:8b` model (see
`.cdr/index/regression.jsonl`'s `hivemind-issue19-3.5.2-F7-llm-json-control-chars`):
even after `strip_code_fences` runs cleanly, ~7/11 real documents in that smoke run
produced completions `json.loads` still rejected, because the model embedded, inside
a JSON string value (typically `content_markdown`):

- raw, unescaped control characters (e.g. a literal newline byte instead of the
  two-character escape sequence `\\n`) -- `json.loads` raises `Invalid control
  character at: ...`.
- a stray `\"\"\"` (Python-docstring-style) triple-quote artifact wrapping the value
  instead of a single pair of `"` -- `json.loads` raises `Expecting ',' delimiter:
  ...` (the first two of the three quote characters get parsed as an empty string,
  leaving the rest as unexpected trailing content).

`sanitize_control_chars_and_triple_quotes` fixes both, and -- like
`strip_code_fences` above -- lives here rather than duplicated inside `segment.py`,
so any other caller needing the same tolerance in the future has one shared home to
import from.
"""

from __future__ import annotations

import json
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


#: Matches a value wrapped in a Python-docstring-style `"""..."""` triple-quote
#: artifact instead of a plain JSON `"..."` string (see module docstring, F7).
#: Non-greedy + DOTALL so each occurrence closes at its own nearest following triple
#: quote rather than spanning multiple fields, and so a raw newline inside the
#: wrapped content doesn't stop the match.
_TRIPLE_QUOTE_RE = re.compile(r'"""(?P<body>.*?)"""', re.DOTALL)

#: JSON's own single-character escapes for the control characters most commonly
#: emitted raw by real models (plain `\uXXXX` covers every other control byte).
_NAMED_CONTROL_ESCAPES = {
    "\n": "\\n",
    "\r": "\\r",
    "\t": "\\t",
    "\b": "\\b",
    "\f": "\\f",
}


def _normalize_triple_quoted_strings(text: str) -> str:
    """Replace each `\"\"\"...\"\"\"`-wrapped span in `text` with a single
    well-formed JSON string built from the same (raw) content.

    `json.dumps` on the captured content re-escapes any control characters or
    literal `"` characters the model left raw inside it, so this also subsumes
    the control-character case for anything caught inside a triple-quoted span.
    """

    def _replace(match: re.Match) -> str:
        return json.dumps(match.group("body"))

    return _TRIPLE_QUOTE_RE.sub(_replace, text)


def _escape_control_chars_in_strings(text: str) -> str:
    """Escape any raw control character found *inside* a `"..."` JSON string
    literal in `text`, leaving everything outside of string literals (including
    legal structural whitespace/newlines between JSON tokens) untouched.

    A small character-scanning state machine, not a blanket regex, precisely
    because JSON's own pretty-printed whitespace *outside* strings is legal and
    must not be touched -- only raw control bytes a model left unescaped *inside*
    a string value are the problem (see module docstring, F7).
    """
    out: list[str] = []
    in_string = False
    i = 0
    n = len(text)
    while i < n:
        ch = text[i]
        if in_string:
            if ch == "\\" and i + 1 < n:
                # An existing (valid) escape sequence -- copy both characters
                # through untouched rather than reinterpreting it.
                out.append(ch)
                out.append(text[i + 1])
                i += 2
                continue
            if ch == '"':
                in_string = False
                out.append(ch)
                i += 1
                continue
            if ord(ch) < 0x20:
                out.append(_NAMED_CONTROL_ESCAPES.get(ch, f"\\u{ord(ch):04x}"))
                i += 1
                continue
            out.append(ch)
            i += 1
        else:
            if ch == '"':
                in_string = True
            out.append(ch)
            i += 1
    return "".join(out)


def sanitize_control_chars_and_triple_quotes(raw: str) -> str:
    """Best-effort repair of `raw` (an LLM completion string, already past
    `strip_code_fences`, that `json.loads` has already failed to parse) for the two
    real-world artifact shapes described in the module docstring (F7): stray
    `\"\"\"`-wrapped string values, and raw control characters embedded directly
    inside a JSON string value.

    Intended to be used only as a fallback retry after a first `json.loads` attempt
    has already raised `json.JSONDecodeError` -- it is a no-op on already-valid JSON
    (neither artifact shape can occur in well-formed JSON), so gating it behind the
    first failure, rather than always applying it, cannot introduce a new regression
    on the already-working happy path; it can only turn some previously-failing
    inputs into successfully-parsed ones.
    """
    normalized = _normalize_triple_quoted_strings(raw)
    return _escape_control_chars_in_strings(normalized)
