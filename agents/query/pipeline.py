"""Full query-pipeline wiring: `refine_intent -> select_top_k ->
expand_insufficient_topics -> combine_and_cap -> synthesize_answer`, in that order.

Per issue #25 subtask 4.6.1 and `docs/LLD/query-agent.md`'s "Pipeline order" section
("query -> intent_refiner -> topic_selector (+ SearchCandidates / GraphNeighbors) ->
synthesizer -> answer"), this module provides the single entry point,
`run_query_pipeline()`, that chains all three agents built by issues #22 (`intent_refiner.py`),
#23 (`topic_selector.py`), and #24 (`synthesizer.py`) in order, and is called by the api/
gateway's `/query` HTTP route (`api/routes/query.go`) via whatever process boundary hosts it
(see "gRPC wiring -- disclosed gap" below).

No real gRPC client wiring built here -- disclosed choice
------------------------------------------------------------
`topic_selector.py`'s own module docstring (issue #23, subtasks 4.4.1/4.4.2) already
discloses that `agents/query/` has no gRPC client wiring yet: `SearchCandidatesFn` and
`GraphNeighborsFn` are documented injection-point type aliases, never called from within
`topic_selector.py` itself, and no real gRPC-backed implementation exists in this package
(unlike `agents/ingestion/`'s `wiring.py`). This module inherits that same gap and follows the
identical, already-established convention: `run_query_pipeline()` takes `search_candidates`
and `graph_neighbors` as injected callables (satisfying `topic_selector.SearchCandidatesFn` /
`GraphNeighborsFn` exactly), rather than constructing a concrete gRPC stub internally. Building
the real `engine/rpc`-backed client channel is out of scope for this subtask and remains a
disclosed gap for a later subtask.

`GetFileFn` -- new injection point, same convention -- disclosed choice
--------------------------------------------------------------------------
`topic_selector.combine_and_cap()`'s own module comment states that "[m]apping `file_id`s back
to file content for the synthesizer prompt is left to a later subtask (out of scope here)" --
this subtask is that later subtask. `synthesizer.synthesize_answer()` needs a single
`selected_markdown` string with `"## File: <path>"` headers (per its own documented format),
but `combine_and_cap()` only returns bare `file_id: int` values. Rather than inventing a new
concrete `GetFile`/`ReadPartial` gRPC client (out of scope, no proto RPC is wired into this
package for the same reason as `SearchCandidates`/`GraphNeighbors` above), this module defines
one more injected callable, `GetFileFn = Callable[[int], tuple[str, str]]` (`file_id -> (path,
content)`), mirroring `SearchCandidatesFn`/`GraphNeighborsFn`'s exact shape and precedent. Real
callers (e.g. a future `agents/query/wiring.py`, mirroring `agents/ingestion/`'s own
`wiring.py`) are expected to satisfy this by calling the engine's real `GetFile`/`ReadPartial`
RPC; tests supply a plain fixture function.

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
#: markdown content, for building `synthesize_answer()`'s `selected_markdown` input. Mirrors
#: `topic_selector.SearchCandidatesFn`/`GraphNeighborsFn`'s exact "documented callable
#: injection point, no real gRPC-backed implementation built here" convention -- see module
#: docstring's "GetFileFn" disclosure.
GetFileFn = Callable[[int], "tuple[str, str]"]

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


def _build_selected_markdown(file_ids: Sequence[int], get_file: GetFileFn) -> str:
    """Resolve each `file_id` in `file_ids` (via `get_file`) into a `"## File: <path>"`-headered
    markdown block, and join them in order.

    Matches `synthesizer.py`'s documented file-path header format
    (`^##\\s*File:\\s*(?P<path>.+?)\\s*$`) exactly, so `synthesize_answer`'s own
    `_extract_provided_paths` can round-trip the paths this function embeds.
    """
    blocks: list[str] = []
    for file_id in file_ids:
        path, content = get_file(file_id)
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
            docstring's "No real gRPC client wiring built here" disclosure.
        graph_neighbors: Callable satisfying `topic_selector.GraphNeighborsFn`, forwarded to
            `expand_insufficient_topics`.
        get_file: Callable satisfying `GetFileFn` (`file_id -> (path, content)`), called once
            per entry in the final selected `file_id` list. See module docstring's `GetFileFn`
            disclosure.
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
    expansions = expand_insufficient_topics(selected, graph_neighbors, hops=hops, ratio=ratio)
    file_ids = combine_and_cap(selected, expansions, k=k)

    if not file_ids:
        raise PipelineError(
            "run_query_pipeline: no candidate files were selected for query "
            f"{query!r}; nothing to synthesize an answer from"
        )

    selected_markdown = _build_selected_markdown(file_ids, get_file)

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
