"""Real `RunQuery` gRPC server: the Python-side half of GitHub issue #56 subtask 4.6.3.2's
`api/main.go` -> `notImplementedPipeline` fix (closing finding F-4.6.1-1).

Per `agents/query/wiring.py`'s module docstring ("What this module does not do"): that
module supplies only the *outbound* RPC calls `agents/query/pipeline.py` makes to the Go
engine (`SearchCandidates`/`GraphNeighbors`/`GetFile`). This module is the *inbound* side --
a real `grpc.Server` implementing `hivemind_pb2_grpc.HiveMindServicer.RunQuery`, so the Go
`api/` gateway's `/query` route (`api/routes/query.go`, via `api/queryclient.GRPCQueryPipeline`)
can invoke `run_query_pipeline()` itself over a real network gRPC call, exactly mirroring
`engine/split/proposer_grpc.go`'s reversed-direction precedent (`ProposeSplit`: Go server,
Python client) but with the roles swapped for `RunQuery` (Python server, Go client).

Only `RunQuery` is implemented here -- `SearchCandidates`/`GraphNeighbors`/`GetFile` remain
served exclusively by `engine/rpc/server.go` (the Go engine), never by this module; this
server exists solely so `api/` has something to dial for the *one* RPC direction the engine
does not itself implement (see `engine/rpc/server.go`'s generated
`UnimplementedHiveMindServer.RunQuery`, which returns `codes.Unimplemented` -- this module is
the real implementation of that same RPC, just running as a separate Python process rather
than inside the Go engine binary).

Construction -- disclosed choices
----------------------------------------------------------------------------------------
`QueryRunQueryServicer.__init__` takes an already-open `grpc.Channel` to the engine (so it
can build `wiring.py`'s three client classes against it) plus an already-constructed
`LLMClient`, rather than constructing either itself -- mirroring `GRPCQueryPipeline`'s own
"owns none of its dependencies' lifecycles" convention (`api/queryclient/grpc.go`'s doc
comment) and keeping this class trivially testable via fakes/monkeypatching, with no gRPC or
network setup required to unit-test the RunQuery method's own logic (request/response
translation, error mapping).

`serve()`/`__main__` wires those two dependencies for real: `ENGINE_GRPC_ADDR` (env var,
default `"localhost:50051"`, matching `engine/rpc`'s own conventional default port used
elsewhere in this repo's smoke tests) is dialed via `grpc.insecure_channel` for the engine
connection, and `llm.factory.create_llm_client()` is used with no explicit `provider` (so it
resolves `LLM_PROVIDER` from the environment, per that factory's own documented config
mechanism) for the `LLMClient`. `QUERY_SERVER_PORT` (env var, default `"50052"`, a distinct
port from the engine's own default so both can run on one host during local/dev testing) is
the port this server itself binds to.
"""

from __future__ import annotations

import os
from concurrent import futures
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    import grpc

    from llm.client import LLMClient

#: Default address (host:port) this server dials to reach the Go engine's own gRPC server
#: (`engine/rpc/server.go`), used only by `serve()`/`__main__` when `ENGINE_GRPC_ADDR` is
#: unset. See module docstring's "Construction -- disclosed choices" section.
DEFAULT_ENGINE_GRPC_ADDR = "localhost:50051"

#: Default port this server itself binds to, used only by `serve()`/`__main__` when
#: `QUERY_SERVER_PORT` is unset.
DEFAULT_QUERY_SERVER_PORT = "50052"


def _import_hivemind_grpc_modules():
    """Import and return `(hivemind_pb2, hivemind_pb2_grpc)`, falling back to adding
    `agents/`'s absolute path onto `sys.path` if the plain top-level import fails.

    Mirrors `agents/query/wiring.py`'s `_import_hivemind_grpc_modules` exactly (see that
    function's docstring for the fallback's rationale).
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


def _hivemind_servicer_base():
    """Return `hivemind_pb2_grpc.HiveMindServicer`, imported lazily (see module docstring
    and `_import_hivemind_grpc_modules`'s docstring for why this is deferred rather than a
    top-level import)."""
    _, hivemind_pb2_grpc = _import_hivemind_grpc_modules()
    return hivemind_pb2_grpc.HiveMindServicer


class QueryRunQueryServicer(_hivemind_servicer_base()):
    """Real `hivemind_pb2_grpc.HiveMindServicer` implementation of the `RunQuery` RPC only.

    Constructed from an already-open `engine_channel` (a `grpc.Channel` to the Go engine's
    own gRPC server) and an already-constructed `llm_client`; builds `wiring.py`'s three
    client classes against `engine_channel` once, at construction time, and reuses them for
    every `RunQuery` call this servicer serves (mirroring `wiring.py`'s own clients, which are
    themselves cheap wrappers holding no per-call state beyond the shared channel).

    This class subclasses the generated `hivemind_pb2_grpc.HiveMindServicer` and overrides
    only `RunQuery`; the base class's other four RPC methods (`SearchCandidates`/
    `GraphNeighbors`/`GetFile`/`ProposeSplit`/`PutSegment`/`ReadPartial`) are left as the
    generated base class's own `UNIMPLEMENTED`-returning stubs, since those RPCs remain
    exclusively served by `engine/rpc/server.go`, never by this module (see module
    docstring) -- no stub method bodies need to be duplicated here for
    `hivemind_pb2_grpc.add_HiveMindServicer_to_server` to accept this class.
    """

    def __init__(self, engine_channel: "grpc.Channel", llm_client: "LLMClient") -> None:
        from query.wiring import (
            GrpcGetFileClient,
            GrpcGraphNeighborsClient,
            GrpcSearchCandidatesClient,
        )

        self._llm_client = llm_client
        self._search_candidates = GrpcSearchCandidatesClient(engine_channel)
        self._graph_neighbors = GrpcGraphNeighborsClient(engine_channel)
        self._get_file = GrpcGetFileClient(engine_channel)

    def RunQuery(self, request, context):
        """Handle one `RunQueryRequest`: run the full pipeline via
        `pipeline.run_query_pipeline`, translate its `QueryPipelineResult` into a
        `RunQueryResponse`.

        Errors from the pipeline (LLM failures, malformed LLM JSON, etc.) are not caught
        here: an uncaught exception raised from a servicer method causes `grpc` to abort the
        RPC with `codes.UNKNOWN` and the exception's `str()` as the status details -- the Go
        client (`api/queryclient/grpc.go`'s `GRPCQueryPipeline.RunQuery`) already treats any
        non-OK gRPC status as an error and `api/routes.NewQueryHandler` already treats any
        non-nil error as a 500, so no additional translation is needed at this layer, mirroring
        `engine/rpc/server.go`'s own convention of letting non-domain errors surface as opaque
        server errors rather than fabricating a taxonomy this subtask's scope does not call for.
        """
        from query.pipeline import run_query_pipeline

        result = run_query_pipeline(
            request.query,
            list(request.history),
            llm_client=self._llm_client,
            search_candidates=self._search_candidates,
            graph_neighbors=self._graph_neighbors,
            get_file=self._get_file,
        )

        hivemind_pb2, _ = _import_hivemind_grpc_modules()
        return hivemind_pb2.RunQueryResponse(
            answer=result.synthesis.answer,
            citations=result.synthesis.citations,
        )


def build_server(
    engine_channel: "grpc.Channel", llm_client: "LLMClient"
) -> "grpc.Server":
    """Construct (but do not start) a real `grpc.Server` with `QueryRunQueryServicer`
    registered for the `RunQuery` RPC only. Split out from `serve()` so tests can build a
    server against a `grpc.Channel` of their own choosing (e.g. one dialed to a
    bufconn-equivalent fixture engine) without going through `serve()`'s env-var-driven
    dependency construction.
    """
    import grpc

    _, hivemind_pb2_grpc = _import_hivemind_grpc_modules()

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    hivemind_pb2_grpc.add_HiveMindServicer_to_server(
        QueryRunQueryServicer(engine_channel, llm_client), server
    )
    return server


def serve() -> None:
    """Build and run the real `RunQuery` gRPC server, blocking until interrupted.

    Dependencies are constructed from environment configuration only (see module
    docstring's "Construction -- disclosed choices" section): `ENGINE_GRPC_ADDR` for the
    engine channel, `llm.factory.create_llm_client()` (no explicit provider, so
    `LLM_PROVIDER` is resolved from the environment) for the `LLMClient`, and
    `QUERY_SERVER_PORT` for this server's own bind port.
    """
    import grpc

    from llm.factory import create_llm_client

    engine_addr = os.environ.get("ENGINE_GRPC_ADDR", DEFAULT_ENGINE_GRPC_ADDR)
    port = os.environ.get("QUERY_SERVER_PORT", DEFAULT_QUERY_SERVER_PORT)

    engine_channel = grpc.insecure_channel(engine_addr)
    llm_client = create_llm_client()

    server = build_server(engine_channel, llm_client)
    server.add_insecure_port(f"[::]:{port}")
    server.start()
    print(f"LISTENING {port}", flush=True)
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
