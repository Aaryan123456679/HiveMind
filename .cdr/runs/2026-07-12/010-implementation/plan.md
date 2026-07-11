# Plan: task-5.3.5

1. Add `agents/eval/run_live_benchmark.py`:
   - `LiveBenchmarkError`, `CostCapExceededError`.
   - `_looks_like_bare_json` helper.
   - `ResilientLLMClient(LLMClient)` -- retry-until-bare-JSON wrapper, only
     ever applied to $0 Ollama-backed clients.
   - `CostCappedInterceptor(LLMInterceptor)` -- fail-closed cost-cap
     enforcement at the single real-money call boundary.
   - `build_smokeserver_binary` / `start_smokeserver` / `stop_smokeserver` --
     real Go engine process lifecycle, mirroring
     `agents/ingestion/test_e2e_smoke.py`'s fixtures.
   - `load_live_corpus` -- loads manifest + normalizes each PDF, preserving
     original-casing topic titles for path construction (constraint a).
   - `LiveHivemindRetriever` -- stateful `HivemindRetrieverFn`
     implementation: reprovisions a fresh engine per checkpoint, ingests via
     real gRPC `put_segment`, retrieves via real `run_query_pipeline`,
     catches `PipelineError` -> `[]` (constraint b), maps `file_id`s back to
     `doc_id`s.
   - `sum_real_cost_usd` helper.
   - `_build_judge_config` -- builds the real (never-wrapped) judge client
     + `CostCappedInterceptor`.
   - `main()` -- `load_dotenv()` first, argparse CLI
     (`--judge-provider`/`--judge-model`/`--cost-cap-usd`/`--out-dir`/
     `--checkpoints`/`--manifest`/`--corpus-dir`/`--k`/`--top-k`), wires
     everything together, calls
     `run_benchmark_with_traversal_precision(...)`, writes results + chart,
     prints real spend summary.

2. Add `agents/eval/test_run_live_benchmark.py`: CI-runnable coverage of
   every pure-Python behavior (retry policy, PipelineError mapping, path
   casing, cost-cap enforcement including the fail-closed "never actually
   called" assertion, judge-never-wrapped invariant across all 3 provider
   choices), with zero real infra/network/`.env` reads.

3. Add `"python-dotenv>=1.0"` to `agents/pyproject.toml`'s dependency list,
   with an explanatory comment.

4. Self-consistency: run `agents/eval/` test suite + `ruff check` on both
   new files; confirm zero regressions and zero lint findings.

5. CDR artifact trail + `.cdr/index/task.jsonl` entry for `task-5.3.5`.

6. Exactly ONE local commit (Problem/Solution/Impact style), no push.

7. Handoff: tell the caller to run `/cdr:verify --subtask 5.3.5` next.
