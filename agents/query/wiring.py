"""Real gRPC-backed implementations of `pipeline.run_query_pipeline`'s injected
`search_candidates`/`graph_neighbors`/`get_file` callables.

Per GitHub issue #56 subtask 4.6.3.1 (a follow-up to issue #25's disclosed finding
F-4.6.1-1: "no real gRPC client exists anywhere connecting the Go api gateway to the
Python `agents/query/` pipeline, nor real implementations injected for the
`search_candidates`/`graph_neighbors`/`get_file` callables"), this module closes that gap
for the three RPCs `agents/query/` calls *out* to the Go engine for: `SearchCandidates`,
`GraphNeighbors`, and `GetFile` (all already implemented server-side by
`engine/rpc/server.go` -- no proto or engine changes are needed here).

Mirrors `agents/ingestion/shortlist.py`'s `GrpcSearchCandidatesClient` precedent exactly
--------------------------------------------------------------------------------------------
`agents/ingestion/shortlist.py` (task 3.4.2) already solved the identical problem for the
ingestion side: a thin class wrapping `hivemind_pb2_grpc.HiveMindStub` over a
caller-supplied `grpc.Channel`, translating the generated response message into this
package's own plain, `grpc`-independent dataclass. This module follows that exact shape for
all three of `pipeline.py`'s injection points, including the same lazy-import helper
(`_import_hivemind_grpc_modules`, duplicated per-package rather than shared, matching
`agents/ingestion/shortlist.py` and `agents/ingestion/wiring.py`'s own precedent of each
defining their own copy) so importing this module never requires `grpc` to be installed
or the generated stubs to exist unless a caller actually constructs one of these classes.

What this module does **not** do -- disclosed, forwarded scope
--------------------------------------------------------------------------------------------
This module supplies the *outbound* RPC calls `agents/query/pipeline.py` makes to the Go
engine. It does not stand up any server, and it does not wire the Go `api/` gateway's
`/query` route (`api/routes/query.go`, `api/main.go`'s `notImplementedPipeline`) to invoke
`run_query_pipeline` itself -- that direction needs a new RPC on `proto/hivemind.proto`
(Python as server, mirroring `ProposeSplit`'s reversed direction) that does not exist today,
and is forwarded to a later sub-subtask (task-4.6.3.2) per this run's handoff.

`GetFileFn`'s shape -- follows pipeline.py's fixed, proto-accurate contract
--------------------------------------------------------------------------------------------
`GrpcGetFileClient.__call__` returns `content` only (`str`, decoded as UTF-8 from the wire's
`bytes`), matching `proto/hivemind.proto`'s real `GetFileResponse{content, version}` message
exactly and `pipeline.GetFileFn`'s now-fixed shape (see `pipeline.py`'s `GetFileFn`
proto-shape fix disclosure, closing F-4.6.1-2). It does not and cannot return a path --
`GetFileResponse` has no such field.

`GraphNeighborsFn`'s `edge_type` translation
--------------------------------------------------------------------------------------------
`proto/hivemind.proto`'s `Neighbor.type` is the `EdgeType` enum (`EDGE_TYPE_UNSPECIFIED`,
`ENTITY_COOCCUR`, `LLM_ASSERTED`, `SPLIT_SIBLING`, `REDIRECT`); `topic_selector.GraphNeighbor`
documents `edge_type` as a plain `str`. `GrpcGraphNeighborsClient` translates via the
generated enum descriptor's own `Name()` lookup (`hivemind_pb2.EdgeType.Name(...)`), so the
returned strings are exactly the proto's own canonical enum names -- no separately
maintained mapping to drift out of sync with `proto/hivemind.proto`.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from query.topic_selector import GraphNeighbor, TopicCandidate

if TYPE_CHECKING:
    import grpc


def _import_hivemind_grpc_modules():
    """Import and return `(hivemind_pb2, hivemind_pb2_grpc)`, falling back to adding
    `agents/`'s absolute path onto `sys.path` if the plain top-level import fails.

    Mirrors `agents.ingestion.shortlist._import_hivemind_grpc_modules` exactly (see that
    function's docstring for the fallback's rationale: this file may be imported either as
    part of the installed `agents` package or directly from within `agents/query/` with only
    `agents/` itself on `sys.path`).
    """
    try:
        import hivemind_pb2
        import hivemind_pb2_grpc
    except ImportError:
        import sys
        from pathlib import Path

        agents_dir = str(Path(__file__).resolve().parent.parent)
        if agents_dir not in sys.path:
            sys.path.insert(0, agents_dir)
        import hivemind_pb2
        import hivemind_pb2_grpc
    return hivemind_pb2, hivemind_pb2_grpc


class GrpcSearchCandidatesClient:
    """Real `SearchCandidatesFn` implementation: calls the engine's `SearchCandidates` RPC
    via `hivemind_pb2_grpc.HiveMindStub` over `channel`, translating the response's
    `CandidateTopic` messages into `topic_selector.TopicCandidate`s.

    `grpc` and the generated stubs are imported lazily inside `__init__`/`__call__` (not at
    module import time), so importing `query.wiring` itself never requires `grpc` to be
    installed. See `agents/ingestion/shortlist.py`'s `GrpcSearchCandidatesClient` for the
    identical precedent this class mirrors.
    """

    def __init__(self, channel: "grpc.Channel") -> None:
        _, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

        self._stub = hivemind_pb2_grpc.HiveMindStub(channel)

    def __call__(self, query: str, max_results: int) -> list[TopicCandidate]:
        hivemind_pb2, _ = _import_hivemind_grpc_modules()

        request = hivemind_pb2.SearchCandidatesRequest(query=query, max_results=max_results)
        response = self._stub.SearchCandidates(request)
        return [
            TopicCandidate(file_id=c.file_id, path=c.path, score=c.score)
            for c in response.candidates
        ]


class GrpcGraphNeighborsClient:
    """Real `topic_selector.GraphNeighborsFn` implementation: calls the engine's
    `GraphNeighbors` RPC via `hivemind_pb2_grpc.HiveMindStub` over `channel`, translating the
    response's `Neighbor` messages into `topic_selector.GraphNeighbor`s.

    See module docstring's "`GraphNeighborsFn`'s `edge_type` translation" section for how
    the wire `EdgeType` enum becomes `GraphNeighbor.edge_type`'s plain `str`.
    """

    def __init__(self, channel: "grpc.Channel") -> None:
        _, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

        self._stub = hivemind_pb2_grpc.HiveMindStub(channel)

    def __call__(self, file_id: int, hops: int) -> list[GraphNeighbor]:
        hivemind_pb2, _ = _import_hivemind_grpc_modules()

        request = hivemind_pb2.GraphNeighborsRequest(file_id=file_id, depth=hops)
        response = self._stub.GraphNeighbors(request)
        return [
            GraphNeighbor(
                file_id=n.target_file_id,
                edge_type=hivemind_pb2.EdgeType.Name(n.type),
                weight=n.weight,
                hop=n.hop,
            )
            for n in response.neighbors
        ]


class GrpcGetFileClient:
    """Real `pipeline.GetFileFn` implementation: calls the engine's `GetFile` RPC via
    `hivemind_pb2_grpc.HiveMindStub` over `channel`, returning the resolved file's content
    only (decoded as UTF-8).

    Matches `proto/hivemind.proto`'s real `GetFileResponse{content, version}` shape exactly
    -- see module docstring's "`GetFileFn`'s shape" section for why no path is (or can be)
    returned here. `version` is not surfaced by this callable either: `pipeline.GetFileFn`'s
    contract is `file_id -> content` only (no caller in this package's current pipeline
    consumes an MVCC version), so exposing it here would be speculative, unused scope.
    """

    def __init__(self, channel: "grpc.Channel") -> None:
        _, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

        self._stub = hivemind_pb2_grpc.HiveMindStub(channel)

    def __call__(self, file_id: int) -> str:
        hivemind_pb2, _ = _import_hivemind_grpc_modules()

        request = hivemind_pb2.GetFileRequest(file_id=file_id)
        response = self._stub.GetFile(request)
        return response.content.decode("utf-8")
