# Self-consistency check (internal sanity only -- NOT verification, per invariant I4)

- `python -m pytest data/test_loaders.py -v` (under `agents/.venv`): 14 passed.
- `python -m pytest agents -q` (under `agents/.venv`): 155 passed, 0 failed -- no
  regression vs. the pre-existing baseline at HEAD (`6a03944`).
- `ruff check data/ agents/`: 0 new findings; 1 pre-existing finding in generated
  `agents/hivemind_pb2_grpc.py` (unowned, unrelated, present before this run).
- `ruff check data/load_bitext.py data/load_enron.py data/test_loaders.py`: clean.
- Validation matrix (above) covers every literal acceptance-criterion clause and the
  test spec from `gh issue view 19`'s 3.5.1 body.
- No modification to any file in the "do not touch" list
  (`agents/ingestion/{segment,wiring,propose_split,shortlist}.py`, `engine/`).
- This is internal build/test-green sanity only; independent verification is
  explicitly deferred to `/cdr:verify` per invariant I4 (self-verification
  prohibited) -- not performed here.
