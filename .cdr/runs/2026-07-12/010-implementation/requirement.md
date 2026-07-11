# Requirement: task-5.3.5 (issue #28)

Live end-to-end benchmark runner against the real Go engine gRPC server and
real synthetic corpus, with real judge providers
(`agents/eval/run_live_benchmark.py`).

Sibling subtasks 5.3.1-5.3.4 and 5.4.1 (already verified/committed) built
the benchmark harness (`agents/eval/run_benchmark.py`, `chart.py`,
`cost_latency.py`, `llm_judge.py`, `traversal_precision.py`) entirely
against mocked/fixture infrastructure -- `run_benchmark.main()` explicitly
refuses to execute live. This subtask completes the suite by adding a real,
permanent CLI entrypoint that actually runs the harness against genuine
infrastructure.

## Scope

1. Build/start the real Go engine gRPC server (`engine/cmd/smokeserver`)
   rooted at a fresh temp directory per corpus-growth checkpoint.
2. Load the real 32-document synthetic corpus
   (`data/synthetic_corpus/generated/manifest.json` + PDFs) via
   `agents/ingestion/normalize_pdf.normalize_pdf`.
3. Ingest each checkpoint's documents into the running engine via
   `agents/ingestion/wiring.GrpcPutSegmentClient.put_segment`.
4. Wire a real `HivemindRetrieverFn` calling
   `agents/query/pipeline.run_query_pipeline` with real gRPC clients from
   `agents/query/wiring.py`.
5. Support `--judge-provider` (ollama|openrouter|gemini) and
   `--judge-model` via `agents/llm/factory.create_llm_client`.
6. Run `run_benchmark.run_benchmark_with_traversal_precision(...)` across
   all corpus-growth checkpoints (20/50/100pct via `build_checkpoints`).
7. Write results via `write_benchmark_results` and render via `chart.py`.
8. Sum real spend via `agents/eval/cost_latency.resolve_cost_usd`; support a
   caller-supplied `--cost-cap-usd` (fail closed).

## Hard constraints

- (a) `put_segment`'s `path` argument MUST use the topic title in original
  casing (`entry["primary_topic"]["title"]`, unmodified) -- engine-side
  `candidatePool` path matching is case-preserving, not case-insensitive.
- (b) `run_query_pipeline` raises `PipelineError` (not empty result) on
  zero-candidate retrieval; any `HivemindRetrieverFn` MUST catch this
  specifically and return `[]`.
- (c) LLMs occasionally violate the strict bare-JSON contract enforced by
  `synthesizer.py`/`llm_judge.py` (never touch those files); implement a
  resilient retry-at-`complete()`-boundary wrapper (bump temperature to 0.3
  on retry, up to 3 attempts).
- (d) The retry wrapper is safe ONLY on $0 local Ollama-backed clients; it
  must NEVER wrap the real paid judge client (openrouter/gemini), since a
  hidden retry would issue extra real paid calls with no corresponding
  cost record.
- (e) No manual `.env` parsing anywhere in this module. Per coordinator
  addendum: `main()` may call the standard `python-dotenv` library's
  `load_dotenv()` once at startup (repo-portability convenience,
  no-op if absent) -- this does not change the security boundary; concrete
  LLM clients still resolve keys exclusively via `os.environ`.

## Acceptance criteria

1. `agents/eval/run_live_benchmark.py` exists as a real argparse CLI with at
   least `--judge-provider`, `--judge-model`, `--cost-cap-usd`, `--out-dir`.
2. `agents/eval/test_run_live_benchmark.py` exists, CI-runnable without real
   infra/paid APIs, consistent with sibling `test_run_benchmark.py`.
3. Cost-accounting integrity constraint (d) is enforced/testable.
4. No `.env` file is manually opened/parsed/sourced anywhere in the new code
   (the sole exception being the declared `load_dotenv()` call per the
   addendum).
5. Follows the same code style/conventions as sibling files in
   `agents/eval/`.

## Delivery

Standard CDR implementation artifact trail, ending in exactly ONE local
commit (no push). No self-verification (invariant I4) -- hand off to
`/cdr:verify --subtask 5.3.5`.
