"""Full query-pipeline wiring: `refine_intent -> select_top_k ->
expand_insufficient_topics -> combine_and_cap -> synthesize_answer`, in that order.

Per issue #25 subtask 4.6.1 and `docs/LLD/query-agent.md`'s "Pipeline order" section
("query -> intent_refiner -> topic_selector (+ SearchCandidates / GraphNeighbors) ->
synthesizer -> answer"), this module provides the single entry point,
`run_query_pipeline()`, that chains all three agents built by issues #22 (`intent_refiner.py`),
#23 (`topic_selector.py`), and #24 (`synthesizer.py`) in order, and is called by the api/
gateway's `/query` HTTP route (`api/routes/query.go`) via whatever process boundary hosts it
(see "gRPC wiring -- disclosed gap" below).

Real gRPC client wiring -- `agents/query/wiring.py` (issue #56 subtask 4.6.3.1)
------------------------------------------------------------------------------
Issue #25 subtask 4.6.1 originally shipped this module with `search_candidates`/
`graph_neighbors`/`get_file` as purely-injected callables and no real gRPC-backed
implementation anywhere in `agents/query/` (disclosed forward as finding F-4.6.1-1).
Issue #56 subtask 4.6.3.1 closes the Python-side half of that gap: `agents/query/wiring.py`
now provides `GrpcSearchCandidatesClient`/`GrpcGraphNeighborsClient`/`GrpcGetFileClient`,
each backed by `hivemind_pb2_grpc.HiveMindStub` over a caller-supplied `grpc.Channel`, and
each satisfying this module's injected-callable shapes exactly (mirroring
`agents/ingestion/shortlist.py`'s `GrpcSearchCandidatesClient` precedent). This module's own
signature is unchanged by that addition -- `run_query_pipeline()` still takes
`search_candidates`/`graph_neighbors`/`get_file` as plain callables, so tests keep injecting
fixture functions with no `grpc` dependency, exactly as before.

What subtask 4.6.3.1 does **not** close (forwarded, still disclosed): wiring a real Go
gRPC/HTTP *client* into `api/main.go` in place of `notImplementedPipeline` requires a new
`proto/hivemind.proto` RPC through which the Go `/query` route can invoke
`run_query_pipeline` itself (no such RPC exists today -- `SearchCandidates`/
`GraphNeighbors`/`GetFile` are engine-served RPCs this module calls *out* to, not a way for
`api/` to call *into* this pipeline). That remains a separate, later sub-subtask
(task-4.6.3.2) per this run's handoff.

`GetFileFn` -- proto-shape fix (issue #56 subtask 4.6.3.1, closes F-4.6.1-2)
--------------------------------------------------------------------------------
Issue #25 subtask 4.6.1 originally defined `GetFileFn = Callable[[int], tuple[str, str]]`
(`file_id -> (path, content)`), asking `get_file` to supply both a path and file content.
Verification of that subtask (`.cdr/runs/2026-07-11/031-verification/verification.json`)
disclosed finding F-4.6.1-2: `proto/hivemind.proto`'s real `GetFileResponse` message has
only `content`/`version` fields -- no `path` -- so no real `GetFile`-backed implementation
of the old `GetFileFn` shape could ever exist; only a fixture/fake could satisfy it.

This subtask fixes that mismatch: `GetFileFn` is now `Callable[[int], str]` (`file_id ->
content` only, matching the real `GetFileResponse` exactly), and `_build_selected_markdown`
sources each `file_id`'s `path` from the already-known `TopicCandidate.path` values
`select_top_k` returned (per this finding's own acceptance wording: "source `path` from
`TopicCandidate.path`, already present on candidates returned by `search_candidates`") via a
`path_by_id` mapping built in `run_query_pipeline` before `combine_and_cap` runs, rather than
asking `get_file`/`GetFileResponse` to invent a field neither has.

Residual gap -- closed by issue #56 subtask 4.6.3.2
----------------------------------------------------
Subtask 4.6.3.1 (above) disclosed a residual gap: `combine_and_cap()`'s own module comment
already documents that its output can include `file_id`s reachable **only** via a
`GraphNeighbors` expansion (never present in `selected`), and neither `GetFileResponse` nor
`Neighbor` carried a `path` field at the time, so `path_by_id` (sourced only from
`TopicCandidate.path`) could not resolve those `file_id`s.

Subtask 4.6.3.2 closes this: `proto/hivemind.proto`'s `GetFileResponse` now additionally
carries `path` (a best-effort reverse pathIndex lookup performed server-side, see
`engine/rpc/server.go`'s `GetFile`/`lookupPathForFileID`), so `GetFileFn` is changed back to
`Callable[[int], tuple[str, str]]` (`file_id -> (path, content)`) -- the *original* issue #25
shape, now legitimately satisfiable by a real `GetFile`-backed implementation
(`agents/query/wiring.py`'s `GrpcGetFileClient`). `_build_selected_markdown` still prefers
`path_by_id` (the cheap, already-known `TopicCandidate.path` for `file_id`s reachable from
`select_top_k`'s own output) to avoid an unnecessary reliance on `get_file`'s path for the
common case, but now falls back to `get_file`'s own returned path -- rather than the
`_UNKNOWN_PATH_TEMPLATE` placeholder -- for `file_id`s absent from `path_by_id`. The
placeholder itself is retained as a final fallback only for the now-narrower case of a
`file_id` with genuinely no indexed path anywhere (e.g. `GetFileResponse.path == ""`, see
`engine/rpc/server.go`'s `GetFile` doc comment for when this can still happen).

Call order (per this subtask's test spec: "assert correct call order ... end-to-end")
------------------------------------------------------------------------------------------
1. `intent_refiner.refine_intent(query, history, llm_client, ...)` -- one LLM call.
2. `search_candidates(intent.refined_intent, max_candidates)` -- one call, per
   `topic_selector.SearchCandidatesFn`'s `(query, max_results)` shape.
3. `topic_selector.select_top_k(candidates, k=k)` -- no external call, pure selection.
4. `topic_selector.expand_insufficient_topics(selected, graph_neighbors, hops=hops,
   ratio=ratio)` -- zero or more `graph_neighbors` calls, one per topic judged insufficient
   (per 4.4.2's own contract; this module does not alter that contract).
5. `topic_selector.combine_and_cap(selected, expansions, k=k)` -- no external call, pure
   combine + cap, returns the final deduplicated `file_id` list.
6. `get_file(file_id)` once per entry in that final `file_id` list, in order, to build
   `selected_markdown`.
7. `synthesizer.synthesize_answer(intent.refined_intent, intent.query_type, intent.entities,
   selected_markdown, llm_client, ...)` -- one LLM call.

Empty-selection handling -- disclosed choice
------------------------------------------------
If `combine_and_cap()` yields an empty `file_id` list (e.g. `search_candidates` returned
nothing), calling `synthesize_answer()` with an empty `selected_markdown` would silently
produce an answer citing no sources at all, which is worse than failing loudly -- this module
raises `PipelineError` instead, so the api/ gateway's `/query` route can surface a clear
error rather than a vacuous 200 response.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Callable, Sequence

from query.intent_refiner import IntentRefinerResult, refine_intent
from query.synthesizer import SynthesizerResult, synthesize_answer
from query.topic_selector import (
    DEFAULT_EXPANSION_HOPS,
    DEFAULT_INSUFFICIENCY_RATIO,
    DEFAULT_K,
    GraphNeighborsFn,
    SearchCandidatesFn,
    combine_and_cap,
    expand_insufficient_topics,
    select_top_k,
)

if TYPE_CHECKING:
    from llm.client import LLMClient

#: Injection point for resolving a `combine_and_cap()`-produced `file_id` back to its path and
#: markdown content, for building `synthesize_answer()`'s `selected_markdown` input.
#: `file_id -> (path, content)` -- matches `proto/hivemind.proto`'s real `GetFileResponse`
#: shape as of issue #56 subtask 4.6.3.2 (`content`/`version`/`path`); see module docstring's
#: "Residual gap -- closed by issue #56 subtask 4.6.3.2" section for why this reverted from the
#: content-only shape subtask 4.6.3.1 temporarily used. `agents/query/wiring.py`'s
#: `GrpcGetFileClient` is the real, gRPC-backed implementation of this shape; tests supply a
#: plain fixture function.
GetFileFn = Callable[[int], tuple[str, str]]

#: Placeholder header path used by `_build_selected_markdown` for a `file_id` reachable only via
#: `GraphNeighbors` expansion (never present in `select_top_k`'s output), for which no message in
#: `proto/hivemind.proto` carries a path at all. See module docstring's residual-gap disclosure.
_UNKNOWN_PATH_TEMPLATE = "(path unknown; file_id={file_id})"

#: Default cap on how many candidates `run_query_pipeline` requests from `search_candidates`
#: (the `max_results` argument of `topic_selector.SearchCandidatesFn`'s `(query, max_results)`
#: shape). Chosen generously relative to `DEFAULT_K` (3) so `select_top_k` has a meaningfully
#: larger pool to choose from, matching `ingestion.shortlist.DEFAULT_POOL_SIZE`'s precedent of
#: naming a pool-size constant distinct from the top-k constant rather than reusing `DEFAULT_K`.
DEFAULT_MAX_CANDIDATES = 20


class PipelineError(Exception):
    """Raised when `run_query_pipeline` cannot proceed to synthesis.

    Currently covers exactly one case: `combine_and_cap()` returned no `file_id`s at all (no
    usable context to synthesize an answer from). See module docstring's "Empty-selection
    handling" disclosure.
    """


@dataclass(frozen=True)
class QueryPipelineResult:
    """The full, end-to-end result of one `run_query_pipeline()` call.

    Attributes:
        intent: The `IntentRefinerResult` produced by `intent_refiner.refine_intent`.
        selected_file_ids: The final, deduplicated, `k + 2k`-capped `file_id` list produced by
            `topic_selector.combine_and_cap`.
        synthesis: The `SynthesizerResult` produced by `synthesizer.synthesize_answer`.
    """

    intent: IntentRefinerResult
    selected_file_ids: list[int]
    synthesis: SynthesizerResult


def _build_selected_markdown(
    file_ids: Sequence[int],
    path_by_id: dict[int, str],
    get_file: GetFileFn,
) -> str:
    """Resolve each `file_id` in `file_ids` into a `"## File: <path>"`-headered markdown block
    (content always via `get_file`; path preferentially via `path_by_id`, falling back to
    `get_file`'s own returned path), and join them in order.

    Matches `synthesizer.py`'s documented file-path header format
    (`^##\\s*File:\\s*(?P<path>.+?)\\s*$`) exactly, so `synthesize_answer`'s own
    `_extract_provided_paths` can round-trip the paths this function embeds.

    Args:
        file_ids: `combine_and_cap()`'s output, in order.
        path_by_id: Maps `file_id -> path` for every `file_id` reachable from
            `select_top_k`'s output (i.e. every `TopicCandidate` `search_candidates`
            returned that survived selection). Preferred over `get_file`'s path (cheaper --
            no extra proto-carried lookup needed) whenever it has an entry.
        get_file: Callable satisfying `GetFileFn` (`file_id -> (path, content)`). Always
            called once per `file_id` for `content`; its returned `path` is used as a
            fallback for any `file_id` absent from `path_by_id` (see module docstring's
            "Residual gap -- closed by issue #56 subtask 4.6.3.2" section).

    Returns:
        The joined markdown blocks. A `file_id` absent from `path_by_id` AND for which
        `get_file` itself returns an empty path (genuinely no indexed path anywhere, see
        `engine/rpc/server.go`'s `GetFile` doc comment) gets `_UNKNOWN_PATH_TEMPLATE`'s
        placeholder header instead of a real path -- see module docstring's residual-gap
        disclosure.
    """
    blocks: list[str] = []
    for file_id in file_ids:
        get_file_path, content = get_file(file_id)
        path = (
            path_by_id.get(file_id)
            or get_file_path
            or _UNKNOWN_PATH_TEMPLATE.format(file_id=file_id)
        )
        blocks.append(f"## File: {path}\n{content}\n")
    return "\n".join(blocks)


def run_query_pipeline(
    query: str,
    history: Sequence[str],
    *,
    llm_client: "LLMClient",
    search_candidates: SearchCandidatesFn,
    graph_neighbors: GraphNeighborsFn,
    get_file: GetFileFn,
    k: int = DEFAULT_K,
    max_candidates: int = DEFAULT_MAX_CANDIDATES,
    hops: int = DEFAULT_EXPANSION_HOPS,
    ratio: float = DEFAULT_INSUFFICIENCY_RATIO,
    model: str | None = None,
    temperature: float = 0.0,
    max_tokens: int | None = None,
    timeout: float | None = None,
) -> QueryPipelineResult:
    """Run the full query pipeline: `refine_intent -> search_candidates -> select_top_k ->
    expand_insufficient_topics -> combine_and_cap -> synthesize_answer`, in that order.

    Args:
        query: The user's raw query text, forwarded to `refine_intent`.
        history: Short conversation history, forwarded to `refine_intent`.
        llm_client: The `LLMClient` used for both the intent-refinement and synthesis LLM
            calls (per `docs/LLD/llm-provider.md`, the same provider-agnostic interface used
            throughout `agents/query/`).
        search_candidates: Callable satisfying `topic_selector.SearchCandidatesFn` -- called
            once as `search_candidates(intent.refined_intent, max_candidates)`. See module
            docstring's "Real gRPC client wiring" section for `agents/query/wiring.py`'s real
            implementation.
        graph_neighbors: Callable satisfying `topic_selector.GraphNeighborsFn`, forwarded to
            `expand_insufficient_topics`.
        get_file: Callable satisfying `GetFileFn` (`file_id -> (path, content)`), called once
            per entry in the final selected `file_id` list. Path is preferentially sourced
            from `TopicCandidate.path` (cheaper), falling back to this callable's own
            returned path for `file_id`s only reachable via `GraphNeighbors` expansion --
            see module docstring's "Residual gap -- closed by issue #56 subtask 4.6.3.2"
            section.
        k: Forwarded to `select_top_k` and `combine_and_cap`. Defaults to `DEFAULT_K` (3).
        max_candidates: The `max_results` argument passed to `search_candidates`. Defaults to
            `DEFAULT_MAX_CANDIDATES` (20).
        hops, ratio: Forwarded to `expand_insufficient_topics`.
        model, temperature, max_tokens, timeout: Forwarded verbatim to both
            `refine_intent` and `synthesize_answer` (and, transitively, to
            `llm_client.complete()`).

    Returns:
        A `QueryPipelineResult` carrying the intent-refinement result, the final selected
        `file_id`s, and the synthesis result.

    Raises:
        LLMError: Propagated unwrapped if either LLM call fails (see `refine_intent`'s and
            `synthesize_answer`'s own `Raises` sections).
        IntentRefinerParseError, SynthesizerParseError: Propagated unwrapped on malformed LLM
            output from the respective step.
        ValueError: Propagated unwrapped from `expand_insufficient_topics`/`combine_and_cap`
            if `hops`/`k` are out of their documented valid ranges.
        PipelineError: If `combine_and_cap` yields no `file_id`s at all (see module docstring's
            "Empty-selection handling" disclosure).
    """
    intent = refine_intent(
        query,
        history,
        llm_client,
        model=model,
        temperature=temperature,
        max_tokens=max_tokens,
        timeout=timeout,
    )

    candidates = list(search_candidates(intent.refined_intent, max_candidates))
    selected = select_top_k(candidates, k=k)
    # Built before combine_and_cap runs: the only reliable path source is select_top_k's own
    # TopicCandidate output (see module docstring's GetFileFn proto-shape fix disclosure).
    path_by_id = {topic.file_id: topic.path for topic in selected}
    expansions = expand_insufficient_topics(selected, graph_neighbors, hops=hops, ratio=ratio)
    file_ids = combine_and_cap(selected, expansions, k=k)

    if not file_ids:
        raise PipelineError(
            "run_query_pipeline: no candidate files were selected for query "
            f"{query!r}; nothing to synthesize an answer from"
        )

    selected_markdown = _build_selected_markdown(file_ids, path_by_id, get_file)

    synthesis = synthesize_answer(
        intent.refined_intent,
        intent.query_type,
        intent.entities,
        selected_markdown,
        llm_client,
        model=model,
        temperature=temperature,
        max_tokens=max_tokens,
        timeout=timeout,
    )

    return QueryPipelineResult(
        intent=intent,
        selected_file_ids=file_ids,
        synthesis=synthesis,
    )
