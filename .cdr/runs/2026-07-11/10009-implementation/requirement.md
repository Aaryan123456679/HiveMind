# Requirement — Subtask 5.2.1 (issue #27, milestone #7 "Phase 5")

## Source
GitHub issue #27, subtask 5.2.1: "Classic vector-RAG baseline: tuned chunker + embedding
index + retrieval."

## Acceptance criteria (from issue body, as relayed by the launching agent)
A real (non-strawman) fixed-size-chunk baseline with:
1. Actual chunk-size/overlap tuning (a deliberate, documented choice — not an arbitrary
   default) for a fixed-size chunker.
2. An embedding-based retrieval step (real vector similarity search over real
   embeddings, not a placeholder/keyword stand-in).

This directly answers the system-wide "vector-RAG baseline could be a strawman that
makes HiveMind's own graph-based retrieval look artificially better by comparison" risk,
explicitly called out in `docs/HLD.md` (`#7 System-wide known risks`) and
`docs/LLD/eval.md` ("Classic vector RAG — fixed-size-chunk baseline. Per system-wide
risk tracking, must be genuinely well-tuned ... not a strawman"). This subtask's own
architecture-discovery step (below) explicitly addresses why the chosen chunker/
embedding-model combination is a fair, competitive baseline.

## Test spec (from issue, as relayed)
`pytest agents/eval/test_vector_rag_baseline.py`: run retrieval against a fixture
corpus + fixture queries, assert reasonable recall on known-relevant chunks.

## Impacted modules (from issue)
- `agents/eval/baselines/vector_rag.py` (new)
- `agents/eval/test_vector_rag_baseline.py` (new)

## Explicit scope boundaries (from launching-agent instructions + `.cdr/memory/pending.md`)
- Fixture corpus + fixture queries only. Do **not** wire this baseline up to run
  against the real synthetic-PDF corpus (5.1.2/5.1.3) or the live Bitext/Enron
  datasets — that is explicitly out of scope for 5.2.1 (reserved for a future
  benchmark-execution subtask, e.g. 5.3.4, which requires separate user go-ahead).
- No OpenRouter/Gemini/any paid embedding API. LLM-backed work in this repo defaults
  to local Ollama; embeddings must be local too.
- Check existing dependencies (`agents/pyproject.toml`) before adding a new one.
- Must be interoperable with the existing `agents/eval/datasets.py`
  (`ingestion.rawdoc.RawDocument`) and `agents/eval/ground_truth.py`
  (`TopicGroundTruth`/`QueryLabel`/`RelevantDoc`) shapes, since a future subtask
  (5.3.4) will run this baseline against the combined dataset + ground truth for the
  real benchmark — this subtask does not need to perform that run itself, but its
  public API shapes should not force a rewrite when that day comes.
- Self-test using real (non-mocked) local embeddings against the fixture corpus, per
  the launching agent's explicit instruction (fixture-based self-test is in scope;
  real-corpus/live-benchmark execution is not).

## Non-goals
- No reranking step (issue's own LLD wording allows it "if time allows"; not required
  for 5.2.1's acceptance criteria, and adding one now would expand scope beyond a
  "chunker + embedding index + retrieval" baseline).
- No LLM-judge answer-quality evaluation, no latency/cost measurement, no
  corpus-growth-checkpoint degradation chart — those belong to later Phase 5 subtasks
  (5.3.x) that actually run the full three-arm benchmark.
