"""Live, real-infrastructure benchmark runner (issue #28, subtask 5.3.5).

Every prior subtask under issue #28 (5.3.1 metrics, 5.3.2 judge scoring, 5.3.3 cost/latency,
5.3.4 the corpus-growth-checkpoint harness itself) was deliberately built and verified
*offline*: `run_benchmark.py`'s own module docstring calls this out explicitly as a "binding
scoping constraint" for that pass, and its `main()` refuses to execute (`RunBenchmarkError`)
rather than touch real infrastructure or spend real money. This module is the authorized,
separately-scoped follow-up that closes that gap for real: it wires the exact same,
already-verified harness (`run_benchmark.run_benchmark`/`run_arm_at_checkpoint`/
`default_arm_specs`, `chart.py`, `traversal_precision.compare_precision_across_checkpoints`)
against genuine infrastructure end-to-end:

1. A real Go engine gRPC server (`engine/cmd/smokeserver`, built via `go build
   ./cmd/smokeserver`), rooted at a fresh temp directory per corpus-growth checkpoint (see
   `LiveHivemindRetriever._reprovision`).
2. The real 32-document synthetic corpus (`data/synthetic_corpus/generated/manifest.json` +
   per-doc PDFs), extracted via `agents.ingestion.normalize_pdf.normalize_pdf`.
3. Real ingestion of each checkpoint's documents via
   `agents.ingestion.wiring.GrpcPutSegmentClient.put_segment`.
4. A real `HivemindRetrieverFn` (`LiveHivemindRetriever`) that calls
   `agents.query.pipeline.run_query_pipeline` using real gRPC clients from
   `agents.query.wiring` (`GrpcSearchCandidatesClient`/`GrpcGraphNeighborsClient`/
   `GrpcGetFileClient`).
5. A real judge client (`ollama`/`openrouter`/`gemini`, selected via `--judge-provider`/
   `--judge-model`), built through `agents.llm.factory.create_llm_client` -- **never** by
   reading `.env` directly anywhere in this module; API keys are resolved purely via
   `os.environ` inside the concrete client classes themselves (existing project convention).
6. `run_benchmark.run_benchmark_with_traversal_precision` across all corpus-growth checkpoints
   (20/50/100pct via `build_checkpoints`), which itself calls both
   `run_benchmark.run_benchmark` and `traversal_precision.compare_precision_across_checkpoints`.
7. Results written via `run_benchmark.write_benchmark_results` and rendered via `chart.py`.
8. Real spend summed via `eval.cost_latency.resolve_cost_usd` across
   `BenchmarkReport.stage_records`, printed, and enforced against a caller-supplied
   `--cost-cap-usd` (fail-closed: `CostCappedInterceptor` refuses any further paid call once
   the running total has reached the cap -- see that class's docstring).

Hard-won design constraints from live debugging this exact integration -- do not deviate
--------------------------------------------------------------------------------------------
(a) **Path/query case-sensitivity**: `engine/rpc/search_candidates.go`'s `candidatePool` uses
    raw, case-preserving `splitTerms`, not the lowercasing `tokenizeTerms` -- `btree.PrefixScan`
    is case-sensitive. The real ground-truth queries (`eval.ground_truth`) embed each
    document's topic title verbatim, original casing. `put_segment(..., path=...)` therefore
    MUST use the topic title in its original casing (`manifest["documents"][i]["primary_topic"]
    ["title"]`, unmodified) -- never lowercased or slugified -- or every query silently
    retrieves zero candidates. See `load_live_corpus`.
(b) **`run_query_pipeline` raises on empty retrieval**: it raises
    `agents.query.pipeline.PipelineError` (not an empty result) when `SearchCandidates` +
    `GraphNeighbors` expansion together surface zero candidates. `LiveHivemindRetriever.__call__`
    catches this specifically and returns `[]`, matching `vector_rag`/`graphrag_lite`'s natural
    cold-miss behavior, rather than letting it abort the whole benchmark run.
(c) **LLMs occasionally violate the bare-JSON contract** enforced by `query.synthesizer`
    (`SynthesizerParseError`) and `eval.llm_judge` (`JudgeError`) -- markdown fences, prose
    prefixes, trailing data. `ResilientLLMClient` retries at the `complete()`/
    `complete_with_usage()` boundary (bumping temperature on retry, up to 3 attempts total),
    picking the first attempt whose text passes a best-effort `json.loads` probe
    (`_looks_like_bare_json`) -- transparent to every caller. `query.synthesizer.py` and
    `eval.llm_judge.py` themselves are never touched or loosened.
(d) **Cost-accounting integrity**: `ResilientLLMClient` is only ever constructed around `$0`
    local Ollama-backed clients in this module (the retrieval/intent-refinement LLM, the
    final-answer LLM, GraphRAG-lite's LLM) -- **never** around the real paid judge client.
    `agents.llm.interceptor.LLMInterceptor.call()` records exactly one cost/usage record per
    logical call; a hidden retry underneath it would issue extra real paid calls with
    `eval.cost_latency.resolve_cost_usd` never reflecting that extra spend. This module never
    wraps the judge client in `ResilientLLMClient`, for any `--judge-provider` value (including
    `ollama`) -- simplest possible way to make this invariant trivially, statically true rather
    than provider-conditional. See `test_run_live_benchmark.py`'s
    `test_judge_client_never_wrapped_in_resilient_client`.
(e) **No manual `.env` parsing**: this module never opens/parses/sources a `.env` file itself
    with hand-rolled code. `main()` does call the well-known, standard `python-dotenv` library's
    `load_dotenv()` once, at startup -- a repo-portability convenience so a fresh clone doesn't
    require manually exporting `OPENROUTER_API_KEY`/`GEMINI_API_KEY` in the shell before a real
    judge-provider run. This does not change the security boundary: `load_dotenv()` only
    pre-populates `os.environ` (a no-op if no `.env` file exists); `OpenRouterClient`/
    `GeminiClient` still resolve keys exclusively via `os.environ`, unchanged, and no key
    material is read, logged, or passed through any code path in this module itself.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import tempfile
import time
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import TYPE_CHECKING

from dotenv import load_dotenv

from eval.baselines.vector_rag import OllamaEmbeddingClient
from eval.chart import write_chart
from eval.cost_latency import StageRecord, resolve_cost_usd
from eval.ground_truth import (
    DEFAULT_MANIFEST_PATH,
    GroundTruthDataset,
    QueryLabel,
    build_ground_truth_dataset,
    load_manifest,
)
from eval.run_benchmark import (
    DEFAULT_CHECKPOINT_PERCENTAGES,
    BenchmarkReport,
    JudgeConfig,
    build_checkpoints,
    default_arm_specs,
    run_benchmark_with_traversal_precision,
    write_benchmark_results,
)
from llm.client import CompletionResult, LLMClient
from llm.factory import create_llm_client
from llm.interceptor import LLMInterceptor

if TYPE_CHECKING:
    import grpc

#: Root of the repo, resolved from this file's own location (agents/eval/run_live_benchmark.py).
_REPO_ROOT = Path(__file__).resolve().parents[2]
_ENGINE_DIR = _REPO_ROOT / "engine"
_AGENTS_DIR = _REPO_ROOT / "agents"

#: Directory holding the real 32-doc synthetic corpus's per-document PDFs, sibling to
#: `eval.ground_truth.DEFAULT_MANIFEST_PATH`.
DEFAULT_CORPUS_DIR = DEFAULT_MANIFEST_PATH.parent

#: Providers whose calls are unconditionally free/local -- matches
#: `llm.interceptor._FREE_PROVIDERS`/`cost_latency._FREE_LOCAL_PROVIDERS`'s own convention.
_FREE_PROVIDERS = frozenset({"ollama"})

#: Retry budget for `ResilientLLMClient` -- see constraint (c) in the module docstring.
_RESILIENT_MAX_ATTEMPTS = 3
#: Temperature used on retry attempts (attempt index >= 1), per constraint (c).
_RESILIENT_RETRY_TEMPERATURE = 0.3


class LiveBenchmarkError(Exception):
    """Base exception for this module's own (non-RPC-transport, non-LLM) failures."""


class CostCapExceededError(LiveBenchmarkError):
    """Raised by `CostCappedInterceptor.call` when the running total spend has already reached
    (or would be pushed past) the caller-supplied cost cap, and a paid call is about to be made.

    Fail-closed by design (module docstring's point 8): the cap check happens *before* the call
    that would exceed it is attempted, so the run stops rather than silently overspending.
    """


def _looks_like_bare_json(text: str) -> bool:
    """Best-effort probe: `True` iff `text.strip()` parses as JSON via `json.loads`.

    Used only to *choose* which retry attempt's text to return (see `ResilientLLMClient`) --
    never to validate/parse on behalf of `query.synthesizer.py`/`eval.llm_judge.py` themselves,
    which retain their own independent, zero-tolerance parsing (see module docstring's
    constraint (c)).
    """
    try:
        json.loads(text.strip())
    except (json.JSONDecodeError, ValueError):
        return False
    return True


class ResilientLLMClient(LLMClient):
    """Transparent retry wrapper around a local, `$0` `LLMClient`, resilient to occasional
    non-bare-JSON completions from small local models (module docstring's constraint (c)).

    MUST only ever wrap a free/local provider's client (constraint (d)) -- never the paid judge
    client. This class does not enforce that itself (it has no way to know what `inner` is
    backed by); callers are responsible for only constructing this around Ollama-backed clients,
    which this module does exclusively (see `main()` and constraint (d)'s test coverage).

    Retries up to `_RESILIENT_MAX_ATTEMPTS` times, bumping `temperature` to
    `_RESILIENT_RETRY_TEMPERATURE` on every attempt after the first, stopping as soon as an
    attempt's text passes `_looks_like_bare_json`. If no attempt passes, returns the *last*
    attempt's text unchanged -- this wrapper never raises on its own account or invents/repairs
    content; it only improves the odds of a clean pass before handing off to the caller's own
    (unmodified) strict parser, which remains free to raise its own `SynthesizerParseError`/
    `JudgeError` on genuinely malformed output.
    """

    def __init__(self, inner: LLMClient) -> None:
        self._inner = inner

    def _attempt_temperature(self, attempt: int, requested_temperature: float) -> float:
        return requested_temperature if attempt == 0 else _RESILIENT_RETRY_TEMPERATURE

    def complete(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> str:
        last_text = ""
        for attempt in range(_RESILIENT_MAX_ATTEMPTS):
            last_text = self._inner.complete(
                prompt,
                model=model,
                temperature=self._attempt_temperature(attempt, temperature),
                max_tokens=max_tokens,
                timeout=timeout,
            )
            if _looks_like_bare_json(last_text):
                return last_text
        return last_text

    def complete_with_usage(
        self,
        prompt: str,
        *,
        model: str | None = None,
        temperature: float = 0.0,
        max_tokens: int | None = None,
        timeout: float | None = None,
    ) -> CompletionResult:
        """Same retry policy as `complete()`, applied to `complete_with_usage()`.

        Not exercised by any real call path in this module today (only free/local clients are
        ever wrapped, and this module never routes a free/local client through
        `LLMInterceptor.call()`, which is the only caller of `complete_with_usage()` -- see
        constraint (d)); provided for defensive completeness/symmetry with `complete()` should a
        future caller wrap a free client used through the interceptor.
        """
        last_result: CompletionResult | None = None
        for attempt in range(_RESILIENT_MAX_ATTEMPTS):
            last_result = self._inner.complete_with_usage(
                prompt,
                model=model,
                temperature=self._attempt_temperature(attempt, temperature),
                max_tokens=max_tokens,
                timeout=timeout,
            )
            if _looks_like_bare_json(last_result.text):
                return last_result
        assert last_result is not None  # loop runs >= 1 time (_RESILIENT_MAX_ATTEMPTS >= 1)
        return last_result


class CostCappedInterceptor(LLMInterceptor):
    """`LLMInterceptor` that fails closed once cumulative recorded spend reaches a cap.

    Every real paid judge call in this module goes through `eval.llm_judge.score_answer`, which
    itself calls `interceptor.call(...)` exclusively (never a bare `client.complete()`) -- so
    this subclass sees every paid call this module could possibly make, and is the single
    enforcement point for `--cost-cap-usd` (module docstring, point 8).

    The check happens *before* delegating to `LLMInterceptor.call` (fail-closed): once
    `total_cost_usd >= cap_usd`, any further non-free-provider call raises
    `CostCapExceededError` immediately, without making the call. A free-provider (`"ollama"`)
    call is never blocked (it costs nothing to begin with).
    """

    def __init__(self, *, cap_usd: float, rate_table: Mapping | None = None) -> None:
        super().__init__(rate_table=rate_table)
        if cap_usd < 0:
            raise ValueError(f"CostCappedInterceptor: cap_usd must be >= 0, got {cap_usd!r}")
        self._cap_usd = cap_usd
        self._total_cost_usd = 0.0

    @property
    def cap_usd(self) -> float:
        return self._cap_usd

    @property
    def total_cost_usd(self) -> float:
        return self._total_cost_usd

    def call(self, client: LLMClient, *, provider: str, arm: str, stage: str, prompt: str, **kwargs):
        if provider.lower() not in _FREE_PROVIDERS and self._total_cost_usd >= self._cap_usd:
            raise CostCapExceededError(
                f"cost cap of ${self._cap_usd:.4f} reached (already spent "
                f"${self._total_cost_usd:.4f}); refusing further paid calls "
                f"(provider={provider!r}, arm={arm!r}, stage={stage!r})"
            )
        intercepted = super().call(client, provider=provider, arm=arm, stage=stage, prompt=prompt, **kwargs)
        self._total_cost_usd += resolve_cost_usd(intercepted.record)
        return intercepted


def build_smokeserver_binary(*, engine_dir: Path = _ENGINE_DIR, build_dir: Path | None = None) -> Path:
    """`go build ./cmd/smokeserver` into `build_dir` (a fresh temp dir if not given), returning
    the built binary's path. Mirrors `agents/ingestion/test_e2e_smoke.py`'s
    `smokeserver_binary` fixture exactly."""
    if build_dir is None:
        build_dir = Path(tempfile.mkdtemp(prefix="smokeserver_bin_"))
    binary_path = build_dir / "smokeserver"
    result = subprocess.run(
        ["go", "build", "-o", str(binary_path), "./cmd/smokeserver"],
        cwd=str(engine_dir),
        capture_output=True,
        text=True,
        timeout=120,
    )
    if result.returncode != 0:
        raise LiveBenchmarkError(
            f"go build ./cmd/smokeserver failed:\n{result.stdout}\n{result.stderr}"
        )
    return binary_path


def start_smokeserver(binary_path: Path, root_dir: Path, *, startup_timeout: float = 15.0):
    """Launch a real `smokeserver` subprocess rooted at `root_dir`, returning `(proc, addr)`
    once it has printed its `"LISTENING <host:port>"` line. Mirrors
    `agents/ingestion/test_e2e_smoke.py`'s `running_engine` fixture exactly."""
    root_dir.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(
        [str(binary_path), "-root", str(root_dir)],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    deadline = time.monotonic() + startup_timeout
    addr = None
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            stderr = proc.stderr.read() if proc.stderr else ""
            raise LiveBenchmarkError(f"smokeserver exited early (code {proc.returncode}): {stderr}")
        line = proc.stdout.readline()
        if line.startswith("LISTENING "):
            addr = line.strip().split(" ", 1)[1]
            break
    if addr is None:
        proc.terminate()
        raise LiveBenchmarkError("smokeserver did not print LISTENING line within startup_timeout")
    return proc, addr


def stop_smokeserver(proc: "subprocess.Popen", *, timeout: float = 10.0) -> None:
    """Gracefully terminate a `smokeserver` subprocess, killing it if it does not exit in time."""
    proc.terminate()
    try:
        proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)


def load_live_corpus(
    manifest_path: str | Path = DEFAULT_MANIFEST_PATH,
    corpus_dir: str | Path = DEFAULT_CORPUS_DIR,
) -> tuple[list[tuple[str, str]], dict[str, str]]:
    """Load the real synthetic corpus: every document's full text (via
    `ingestion.normalize_pdf.normalize_pdf`) plus its primary topic's title in original casing.

    Returns:
        `(all_docs, doc_titles)`:
          - `all_docs`: `[(doc_id, text)]` in manifest order, matching `build_checkpoints`'s
            expected input shape.
          - `doc_titles`: `{doc_id: primary_topic_title}`, title in the manifest's **original**
            casing -- required by `LiveHivemindRetriever` to satisfy constraint (a) (never
            lowercase/slugify this value before passing it as `put_segment`'s `path`).
    """
    from ingestion.normalize_pdf import normalize_pdf

    manifest = load_manifest(manifest_path)
    corpus_dir = Path(corpus_dir)

    all_docs: list[tuple[str, str]] = []
    doc_titles: dict[str, str] = {}
    for entry in manifest["documents"]:
        doc_id = entry["doc_id"]
        pdf_path = corpus_dir / entry["filename"]
        normalized = normalize_pdf(pdf_path)
        all_docs.append((doc_id, str(normalized)))
        # Original casing, unmodified -- see module docstring's constraint (a).
        doc_titles[doc_id] = entry["primary_topic"]["title"]

    return all_docs, doc_titles


class LiveHivemindRetriever:
    """Real `run_benchmark.HivemindRetrieverFn` implementation: for each distinct checkpoint
    corpus it is called with, provisions a fresh `smokeserver` instance rooted at a new temp
    directory, ingests exactly that checkpoint's documents via `PutSegment`, then answers every
    query for that checkpoint via `query.pipeline.run_query_pipeline` over real gRPC clients.

    `run_benchmark.default_arm_specs`'s own `_build_hivemind_retriever_factory` calls this
    module's injected `hivemind_retriever` once per query, always with the *current*
    checkpoint's corpus mapping as the second argument (not once per checkpoint) -- this class
    itself detects a checkpoint change (by comparing the given corpus's key set against the one
    it last provisioned for) and only re-provisions the engine when the corpus actually changes,
    so a checkpoint's engine is built once and reused for all of that checkpoint's queries.
    """

    def __init__(
        self,
        smokeserver_binary: Path,
        doc_titles: Mapping[str, str],
        retrieval_llm_client: LLMClient,
        *,
        work_dir: Path | None = None,
    ) -> None:
        self._smokeserver_binary = smokeserver_binary
        self._doc_titles = doc_titles
        self._retrieval_llm_client = retrieval_llm_client
        self._work_dir = work_dir or Path(tempfile.mkdtemp(prefix="live_hivemind_"))

        self._current_signature: frozenset[str] | None = None
        self._proc = None
        self._channel: "grpc.Channel | None" = None
        self._doc_id_by_file_id: dict[int, str] = {}
        self._search_candidates = None
        self._graph_neighbors = None
        self._get_file = None
        self._checkpoint_index = 0

    def _teardown_current(self) -> None:
        if self._channel is not None:
            self._channel.close()
            self._channel = None
        if self._proc is not None:
            stop_smokeserver(self._proc)
            self._proc = None
        self._doc_id_by_file_id = {}

    def _reprovision(self, corpus: Mapping[str, str]) -> None:
        import grpc

        from ingestion.wiring import GrpcPutSegmentClient
        from query.wiring import GrpcGetFileClient, GrpcGraphNeighborsClient, GrpcSearchCandidatesClient

        self._teardown_current()

        self._checkpoint_index += 1
        root_dir = self._work_dir / f"checkpoint-{self._checkpoint_index}"
        proc, addr = start_smokeserver(self._smokeserver_binary, root_dir)
        self._proc = proc
        self._channel = grpc.insecure_channel(addr)

        put_segment_client = GrpcPutSegmentClient(self._channel)
        for doc_id, text in corpus.items():
            title = self._doc_titles[doc_id]
            result = put_segment_client.put_segment(0, text.encode("utf-8"), title)
            self._doc_id_by_file_id[result.file_id] = doc_id

        self._search_candidates = GrpcSearchCandidatesClient(self._channel)
        self._graph_neighbors = GrpcGraphNeighborsClient(self._channel)
        self._get_file = GrpcGetFileClient(self._channel)
        self._current_signature = frozenset(corpus.keys())

    def __call__(self, query_label: QueryLabel, corpus: Mapping[str, str]) -> list[str]:
        from query.intent_refiner import IntentRefinerError
        from query.pipeline import PipelineError, run_query_pipeline
        from query.synthesizer import SynthesizerError

        signature = frozenset(corpus.keys())
        if signature != self._current_signature:
            self._reprovision(corpus)

        try:
            result = run_query_pipeline(
                query_label.query,
                [],
                llm_client=self._retrieval_llm_client,
                search_candidates=self._search_candidates,
                graph_neighbors=self._graph_neighbors,
                get_file=self._get_file,
            )
        except PipelineError:
            # Constraint (b): a cold miss (zero candidates surfaced) raises PipelineError
            # instead of returning an empty result -- map it to [] here, matching
            # vector_rag/graphrag_lite's own natural empty-result behavior on a cold miss.
            return []
        except (SynthesizerError, IntentRefinerError):
            # The local retrieval-side LLM can still emit non-bare-JSON, or
            # semantically-invalid-but-JSON-valid output (e.g. an entity given
            # as a dict instead of a string), even after ResilientLLMClient's
            # retries are exhausted -- treat any such strict-parser failure
            # anywhere in the pipeline (intent refinement or synthesis) as a
            # retrieval miss rather than crashing the whole live run over one
            # bad query.
            return []

        return [
            self._doc_id_by_file_id[file_id]
            for file_id in result.selected_file_ids
            if file_id in self._doc_id_by_file_id
        ]

    def close(self) -> None:
        """Tear down whatever engine instance is currently running. Call once at the end of a
        full benchmark run."""
        self._teardown_current()


def sum_real_cost_usd(stage_records: Sequence[StageRecord]) -> float:
    """Sum `eval.cost_latency.resolve_cost_usd` across every record -- the real total spend for
    a completed (or partially completed) run (module docstring, point 8)."""
    return sum(resolve_cost_usd(record) for record in stage_records)


def _build_judge_config(
    *,
    judge_provider: str,
    judge_model: str | None,
    cost_cap_usd: float,
    final_answer_llm_client: LLMClient,
) -> tuple[JudgeConfig, CostCappedInterceptor]:
    """Build the real `JudgeConfig` for one live run.

    The judge client itself (`judge_llm_client`) is **never** wrapped in `ResilientLLMClient` --
    see module docstring's constraint (d) and `test_run_live_benchmark.py`'s
    `test_judge_client_never_wrapped_in_resilient_client`.
    """
    judge_kwargs: dict[str, object] = {}
    if judge_model is not None:
        judge_kwargs["model"] = judge_model
    judge_llm_client = create_llm_client(provider=judge_provider, **judge_kwargs)

    interceptor = CostCappedInterceptor(cap_usd=cost_cap_usd)
    judge_config = JudgeConfig(
        final_answer_llm_client=final_answer_llm_client,
        judge_llm_client=judge_llm_client,
        interceptor=interceptor,
        provider=judge_provider,
        model=judge_model,
    )
    return judge_config, interceptor


def main(argv: list[str] | None = None) -> None:
    """Real, live, paid-API-capable end-to-end benchmark CLI.

    Unlike `run_benchmark.main()` (which deliberately refuses to execute), this entry point
    genuinely builds the Go engine, ingests the real corpus, and calls out to a real judge
    provider -- see the module docstring for the full wiring and its hard-won constraints.

    A gitignored `.env` file at the repo root (if present) is auto-loaded via `python-dotenv`'s
    `load_dotenv()` before any client is constructed, so `OPENROUTER_API_KEY`/`GEMINI_API_KEY`
    (or any other variable `OpenRouterClient`/`GeminiClient` look up) do not need to be exported
    manually in the shell -- see module docstring's constraint (e). This is a no-op if no `.env`
    file exists.
    """
    load_dotenv()

    parser = argparse.ArgumentParser(
        description=(
            f"{__doc__.splitlines()[0]} A gitignored .env file at the repo root, if present, is "
            "auto-loaded for OPENROUTER_API_KEY/GEMINI_API_KEY/etc via python-dotenv."
        )
    )
    parser.add_argument(
        "--checkpoints",
        default=",".join(str(p) for p in DEFAULT_CHECKPOINT_PERCENTAGES),
        help="Comma-separated ingestion percentages, e.g. '20,50,100'.",
    )
    parser.add_argument("--manifest", default=str(DEFAULT_MANIFEST_PATH))
    parser.add_argument("--corpus-dir", default=str(DEFAULT_CORPUS_DIR))
    parser.add_argument(
        "--judge-provider",
        choices=("ollama", "openrouter", "gemini"),
        default="ollama",
        help="Real judge provider to score final answers with.",
    )
    parser.add_argument("--judge-model", default=None, help="Optional judge model override.")
    parser.add_argument(
        "--cost-cap-usd",
        type=float,
        default=1.0,
        help="Hard USD spend cap enforced fail-closed by CostCappedInterceptor.",
    )
    parser.add_argument("--out-dir", default="agents/eval/live_benchmark_results")
    parser.add_argument("--k", type=int, default=5)
    parser.add_argument("--top-k", type=int, default=5)
    args = parser.parse_args(argv)

    percentages = [int(p.strip()) for p in args.checkpoints.split(",") if p.strip()]

    dataset: GroundTruthDataset = build_ground_truth_dataset(manifest_path=args.manifest)
    all_docs, doc_titles = load_live_corpus(
        manifest_path=args.manifest, corpus_dir=args.corpus_dir
    )
    checkpoints = build_checkpoints(all_docs, percentages)

    smokeserver_binary = build_smokeserver_binary()

    # Every non-judge LLM call in this run is local/free Ollama, wrapped for resilience against
    # occasional non-bare-JSON output (constraint (c)) -- never the judge client (constraint (d)).
    retrieval_llm_client: LLMClient = ResilientLLMClient(create_llm_client(provider="ollama"))
    final_answer_llm_client: LLMClient = ResilientLLMClient(create_llm_client(provider="ollama"))
    graphrag_llm_client: LLMClient = ResilientLLMClient(create_llm_client(provider="ollama"))
    embed_client = OllamaEmbeddingClient()

    hivemind_retriever = LiveHivemindRetriever(
        smokeserver_binary, doc_titles, retrieval_llm_client
    )

    arm_specs = default_arm_specs(
        hivemind_retriever=hivemind_retriever,
        embed_client=embed_client,
        graphrag_llm_client=graphrag_llm_client,
        top_k=args.top_k,
    )

    judge_config, interceptor = _build_judge_config(
        judge_provider=args.judge_provider,
        judge_model=args.judge_model,
        cost_cap_usd=args.cost_cap_usd,
        final_answer_llm_client=final_answer_llm_client,
    )

    try:
        report: BenchmarkReport = run_benchmark_with_traversal_precision(
            checkpoints,
            dataset.queries,
            arm_specs,
            graphrag_llm_client,
            k=args.k,
            top_k=args.top_k,
            judge_config=judge_config,
        )
    finally:
        hivemind_retriever.close()

    out_dir = Path(args.out_dir)
    results_path = out_dir / "live_benchmark_results.json"
    chart_path = out_dir / "chart.txt"
    write_benchmark_results(report, results_path)
    write_chart(report.to_json()["rows"], chart_path)

    total_cost_usd = sum_real_cost_usd(report.stage_records)
    print(f"Wrote live benchmark results to {results_path}")
    print(f"Wrote degradation chart to {chart_path}")
    print(
        f"Real spend this run: ${total_cost_usd:.4f} "
        f"(interceptor-tracked: ${interceptor.total_cost_usd:.4f}, "
        f"cap: ${args.cost_cap_usd:.4f})"
    )


if __name__ == "__main__":
    main()
