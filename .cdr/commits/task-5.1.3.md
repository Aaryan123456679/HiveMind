# task-5.1.3: Finalize ground-truth topics/queries for benchmark harness (issue #26)

## Summary

Issue #26 (milestone #7, Phase 5) requires the synthetic PDF corpus (subtask
5.1.2) to be seeded with "~30-50 predefined ground-truth topics and
deliberate cross-topic references," with "topic/query labels attached to
[the] dataset" for recall/precision measurement. Subtask 5.1.2 deliberately
shipped only a 10-topic demonstrative seed set pending 5.1.3's own
topic-set decisions, and no ground-truth derivation code existed yet.
Subtask 5.1.3 (previously BLOCKED on OQ-2: curation/verification process)
closes that gap: the user resolved OQ-2 as "auto-derive ground truth from
the generation config (topics.yaml + manifest.json), verified by a manual
spot-check of a random subset against the actual rendered PDFs." This is
issue #26's final subtask.

## Features

- **`data/synthetic_corpus/topics.yaml`** expanded from 10 to 32 topics
  (within the ~30-50 LLD target), spanning HR, security, IT, finance, and
  compliance policy domains. Full corpus regenerated end to end via the
  existing, unchanged (byte-for-byte) `data/gen_synthetic_pdfs.py`
  Ollama-only path -- 32 real PDFs + `manifest.json` committed as the
  benchmark corpus artifact.
- **`agents/eval/ground_truth.py`**: auto-derives ground truth purely
  structurally from `manifest.json`, introducing zero hand-authored
  relevance judgments. `derive_topic_ground_truth()` marks a document
  "primary" for its own seeded topic and "cross_reference" for every topic
  it was deliberately seeded to reference. `derive_query_set()` generates
  one deterministic query per topic (template-based, zero LLM calls per
  OQ-2's resolution) carrying the same relevance judgments. `RelevantDoc`'s
  `doc_id` is deliberately unnamespaced so a future subtask could extend it
  with Bitext/Enron ids for combined-dataset ground truth -- this subtask
  does not populate any such cross-arm judgments (disclosed scope
  boundary: Bitext/Enron are real-world unlabeled corpora with no
  topic-seeding process to derive from). Derived
  `data/synthetic_corpus/ground_truth.json` (32 topics, 32 queries)
  committed as the ground-truth label file.
- **`agents/eval/test_ground_truth.py`**: manifest loading/validation,
  derivation correctness, on-disk schema validation (round-trip fidelity,
  malformed-input error handling, referential integrity against real
  manifest `doc_id`s), plus tests against the actual committed
  `manifest.json`/`ground_truth.json`.
- Mandated manual spot-check performed by the implementer (7 of 32 topics,
  seed=42) and independently re-performed by the verifier (5 topics,
  seed=7, one overlapping topic) against real rendered PDF text.

## Impact

- 2 new files (`agents/eval/ground_truth.py`,
  `agents/eval/test_ground_truth.py`), 1 modified file
  (`data/synthetic_corpus/topics.yaml`, 10->32 topics), 32 regenerated PDFs
  + `manifest.json` + `ground_truth.json` committed as real corpus/label
  artifacts.
- `agents/eval/`: 32/32 tests pass (6 pre-existing + 26 new). `data/`:
  34/34 tests pass, unaffected by the topic-count expansion. `ruff check`:
  clean.
- Full `agents/` regression independently re-run by the verifier: 374
  passed, 0 failed, both with the commit's changes present and after
  reverting every touched file -- confirmed zero regression from this diff
  either way.
- This is issue #26's final subtask; all three subtasks (5.1.1, 5.1.2,
  5.1.3) are now implemented and independently verified.
- Two non-blocking findings surfaced during verification (see Release
  Notes); no false seeded ground-truth claim was found in either spot-check
  sample.

## Verification

- **Verdict:** PASS_WITH_COMMENTS
- **Run ID:** `.cdr/runs/2026-07-11/10006-verification`
- Commit: `ba272fd96ca53c15e8724ed553fe70abb742877d`
- Zero blocking findings. Verifier independently re-parsed `topics.yaml`
  (32 topics, 1:1 with `manifest.json`'s 32 documents and 32 PDFs on disk),
  confirmed `data/gen_synthetic_pdfs.py` has an empty diff and still
  hardcodes `provider="ollama"`, read `ground_truth.py` in full and
  confirmed it makes zero network/LLM/subprocess calls, independently
  re-ran all 26 new tests plus the full `agents/`/`data/` regression
  suites, and independently re-performed the mandated spot-check with a
  different random seed.

## Release Notes

- Added `agents/eval/ground_truth.py` and
  `agents/eval/test_ground_truth.py`; expanded
  `data/synthetic_corpus/topics.yaml` from 10 to 32 topics; regenerated and
  committed the full 32-document synthetic PDF corpus, `manifest.json`, and
  the new `ground_truth.json` label file. `ground_truth.json`'s query set
  (one deterministic query per topic, each with `primary`/`cross_reference`
  relevance labels) satisfies docs/LLD/eval.md's "topic/query labels
  attached to this dataset for recall/precision measurement" requirement as
  an input to a downstream recall/precision@k computation; actual metric
  computation is out of this subtask's disclosed scope.
- **Non-blocking finding** (`.cdr/index/regression.jsonl`, id
  `hivemind-issue26-5.1.3-spot-check-undercount`, low severity, open): an
  independent re-spot-check (different random seed, one overlapping topic)
  found additive LLM hallucinations (invented non-seeded policy names in
  the rendered PDF prose) at higher density than the implementer disclosed,
  including one instance (`doc-vendor-onboarding.pdf`, 3 invented names)
  that the implementer's own sampled set included but their
  self-consistency report did not flag. No false seeded ground-truth claim
  was found in either sample -- this affects confidence in the spot-check
  process's rigor, not the correctness of `ground_truth.py`'s structural
  derivation.
- **Non-blocking finding** (`.cdr/index/regression.jsonl`, id
  `hivemind-issue26-5.1.3-unreproducible-protobuf-claim`, low severity,
  open): the implementer's claimed "363 passed, 8 failed + 2 collection
  errors (pre-existing protobuf gencode/runtime mismatch)" full-suite
  result did not reproduce for the verifier; the shared venv's protobuf
  package has since drifted to 6.33.6, and the suite is fully green (374
  passed, 0 failed) both with and without this commit's changes present.
  No actual regression -- the specific claimed failure signature is simply
  no longer observable due to unrelated environment drift.
- This closes subtask 5.1.3, the final subtask under issue #26 (milestone
  #7, Phase 5). All three subtasks (5.1.1, 5.1.2, 5.1.3) are now
  implemented and independently verified PASS_WITH_COMMENTS; issue #26 is
  eligible for closure.
