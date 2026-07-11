# Plan — 4.5.15.2

1. Append a new section to `agents/ingestion/test_normalize_pdf.py`, after
   the existing F3 trust-boundary section (after line 201), titled
   `# --- 4.5.15.2 (issue #53): Unicode round-trip regression ---`.
2. Add test function(s) matched by `-k unicode_round_trip`:
   - `test_unicode_round_trip_accented_cjk_and_emoji_survive_iter_pages`:
     build a string containing accented Latin (e.g. "café", "naïve"), CJK
     (e.g. "漢字テスト", "北京"), and an astral-plane emoji (e.g. "😀"
     U+1F600, and optionally a ZWJ emoji sequence like "👨‍👩‍👧‍👦" to also
     stress combining/multi-codepoint clusters). Round-trip through
     `_page_marker(1, unicode_text)` -> `iter_pages(...)` and assert the
     recovered text equals the original text as normalized by
     `_page_marker` (i.e. with trailing `\n` appended, matching existing
     test conventions like `test_page_text_containing_its_own_close_marker_survives_round_trip`).
   - A second small test with multiple pages of different unicode content
     to also confirm marker boundaries aren't corrupted by multi-byte
     content spanning a boundary (`test_unicode_round_trip_multiple_pages_preserve_boundaries`),
     matched by the same `-k unicode_round_trip` substring via a shared
     naming prefix.
3. No changes to `normalize_pdf.py` or any other file.
4. Self-consistency: run
   `pytest agents/ingestion/test_normalize_pdf.py -k unicode_round_trip -v`
   and the full `pytest agents/ingestion/test_normalize_pdf.py -v` to confirm
   no regressions in the 12 pre-existing tests (per 4.5.15.1's handoff note
   of "agents/ingestion/test_normalize_pdf.py -v 12"), plus lint (ruff) if
   configured.
5. One commit, Problem/Solution/Impact format, local only (no push).
6. Hand off to `/cdr:verify` — do not self-verify.
