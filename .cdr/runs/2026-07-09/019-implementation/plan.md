# Plan — normalize_email.py

## Parsing tool
`email.parser.Parser` / `email.message_from_string` (stdlib) via `email.parser.BytesParser` or
`Parser`, using `email.policy.default` (modern `EmailMessage` API, gives cleaner header decoding,
`.get_content()` for body text) — no third-party dependency, matches LLD's "via stdlib / Enron-
specific parsing" and grep-confirmed no existing hand-rolled parser precedent to diverge from.

## Public API
```python
@dataclass(frozen=True)
class EmailFields:
    sender: str
    subject: str
    thread: str
    body: str

def normalize_email(path: str | Path) -> EmailFields: ...
```
- `sender`: decoded `From` header, stripped of surrounding whitespace/quoting artifacts via
  `email.utils.parseaddr` when it yields a usable value, else the raw decoded header text
  (so a "Name <addr>" or bare-address form both resolve to a sensible, non-empty string) --
  matches the acceptance criterion "extracts sender ... correctly" without overspecifying
  address-only vs. display-name-included, since Enron headers are inconsistent about this.
- `subject`: decoded `Subject` header as-is (empty string if absent, since a folder-management
  or automated email might legitimately have none).
- `body`: `msg.get_content()` (policy.default) — decoded plain-text body; Enron corpus messages
  are all plain text, no multipart handling needed for a defensible v1 (documented as an explicit
  simplification; multipart/HTML is not present in the real corpus's bulk of employee messages).
- `thread`: see derivation below.

## Thread-id derivation (disclosed judgment call)
The real Enron corpus has **no native, universally-populated thread-id header**. Two-tier
derivation, in priority order:
1. If `In-Reply-To` is present, use it directly (it already identifies the parent message,
   which is the standard signal a threading implementation keys off).
2. Else if `References` is present, use the **first** message-id in the References chain (the
   root of the thread) — more useful for grouping than the last id, which is just the immediate
   parent again.
3. Else (the common case for this corpus): derive a synthetic thread key from the **normalized
   subject** — lowercase, strip a leading run of `Re:`/`Fwd:`/`Fw:` (any casing, repeated),
   collapse internal whitespace, strip surrounding whitespace. This is a standard, defensible
   fallback used by many email-threading/normalization implementations when no explicit
   threading headers exist (e.g. Gmail-style "same normalized subject == same conversation"
   heuristic), and is clearly documented as an approximation, not a true thread id, in both the
   module docstring and the dataclass field docstring.

This is disclosed prominently in the module docstring so any downstream consumer (3.3.4) or
reviewer knows tier 3 is a heuristic, not a guaranteed-unique conversation id.

## Fixtures (hand-authored, not real corpus data)
Confirmed via `find`/`grep` no real Enron sample files exist anywhere in this repo (`data/`
currently only has a planning `README.md` for issue #19). Three small hand-authored `.txt`
fixtures under `agents/ingestion/testdata/`, matching real Enron maildir header conventions:
1. `enron_sample_1.txt` — a normal message with Message-ID/Date/From/To/Subject/X-cc/X-bcc/
   X-Folder/X-Origin/X-FileName and a short body. No In-Reply-To/References (typical case) ->
   exercises subject-normalization thread fallback.
2. `enron_sample_2_reply.txt` — a "Re:" reply with an `In-Reply-To` header present -> exercises
   tier-1 thread derivation, and exercises the `Re:` subject-stripping is *not* what's used here
   (since In-Reply-To wins) but is still asserted for consistency/sanity.
3. `enron_sample_3_no_optional_headers.txt` — minimal headers only (Message-ID, Date, From, To,
   Subject, body) with Cc/Bcc/X-* headers omitted entirely -> exercises graceful handling of
   optional/missing headers (common in the real corpus where not every message has cc/bcc).

## Test cases (see validation-matrix.json)
1. Sender extracted correctly (address, or address+display-name source).
2. Subject extracted correctly, including a `Re:`-prefixed subject.
3. Thread id: tier-3 fallback (no In-Reply-To/References) yields expected normalized-subject key.
4. Thread id: tier-1 (In-Reply-To present) takes priority and is used verbatim.
5. Body text extracted, matches fixture's body content.
6. Missing optional headers (Cc/Bcc/X-*) do not raise; core four fields still populate.
7. Two messages whose subjects differ only by `Re:`/case/whitespace normalize to the same
   thread key (fallback-tier grouping behavior sanity check).
8. Nonexistent file path raises.

## Out of scope
Same boundaries as 3.3.1: no RawDocument wrapping, no dispatch, no ticket normalizer, no directory
batch ingestion, no real dataset staging.
