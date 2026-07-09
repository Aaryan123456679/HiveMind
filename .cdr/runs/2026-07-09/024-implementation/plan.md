# Plan

1. Reproduce the bug against current `normalize_ticket.py` before touching code:
   construct a `TicketComment` whose body contains a literal `[[/COMMENT 1]]\n[[COMMENT 2]]...`
   substring and confirm the rendered blob has an extra `[[COMMENT` occurrence relative
   to the actual comment count (matches verification's reproduction).
2. Change `_comment_block` in `agents/ingestion/normalize_ticket.py` to emit
   `[[COMMENT n LEN=k]]\n<payload>[[/COMMENT n]]`, where `k = len(payload)` and
   `payload = f"AUTHOR: {author}\nBODY:\n{body}\n"` — mirrors
   `normalize_pdf._page_marker`'s exact approach (character-count based, same
   Unicode-code-point-length reasoning already re-verified as Unicode-safe for
   `normalize_pdf.py`).
3. No parser (`iter_comments()`) is added: nothing downstream consumes ticket blobs
   yet (dispatch/`RawDocument` builder is issue 3.3.4, not yet built), unlike 3.3.1
   which added `iter_pages()` because segmentation already needed to consume
   `normalize_pdf` output. The LEN prefix is still embedded at render time so the
   checked-in format itself is unambiguous and future-proof, closing the
   verification's "checked-in contract" concern at the rendering-correctness level
   even without a consumer yet.
4. Update the module docstring (top-of-file overview, marker-format example with
   correct LEN values, and the "reliably parse ... back out" claim) to reflect the
   length-prefixed format and its collision-safety property.
5. Update `test_blob_comments_rendered_as_marker_blocks_in_order` (previously matched
   the literal `[[COMMENT 1]]` string) to match the new `[[COMMENT 1 LEN=...]]` header.
6. Add three new tests: header LEN correctness, own-marker-lookalike survival, and
   other-comment-marker-lookalike survival — directly modeled on
   `test_normalize_pdf.py`'s `test_page_text_containing_its_own_close_marker_survives_round_trip`
   and `test_page_text_containing_other_pages_marker_lookalike_survives`.
7. Run `test_normalize_ticket.py` and the full `agents/ingestion/` suite 3x via
   `agents/.venv`, plus `ruff check`.
8. One local commit (no push), update `.cdr/index/regression.jsonl` with a resolved
   entry, write this run's artifacts, hand off for re-verification.
