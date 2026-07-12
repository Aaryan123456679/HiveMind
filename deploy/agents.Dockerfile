# deploy/agents.Dockerfile -- GitHub issue #31 subtask 6.2.1.
#
# Containerizes agents/ (the Python ML/agent service). The one long-running server process
# in agents/ is agents/query/server.py's QueryRunQueryServicer (the Python side of the
# api/ <-> agents/ gRPC boundary, GitHub issue #56): it implements the RunQuery RPC only,
# dialing the Go engine's own gRPC server (ENGINE_GRPC_ADDR) as a client and binding its own
# port (QUERY_SERVER_PORT). It must run as `python -m query.server` from inside agents/
# (relative imports require agents/ on sys.path -- see architecture-discovery.md).
#
# Build context MUST be the repo root, e.g.:
#   docker build -f deploy/agents.Dockerfile -t hivemind-agents .
#
# No protoc/system compiler deps needed at runtime: agents/hivemind_pb2*.py stubs are
# already committed/generated, and protobuf/grpcio both ship manylinux wheels for
# python:3.11-slim -- matches agents/.venv's local Python 3.11.7 and
# agents/pyproject.toml's `requires-python = ">=3.11"`.

FROM python:3.11-slim
WORKDIR /app
COPY agents/ ./agents/
WORKDIR /app/agents
RUN pip install --no-cache-dir .

ENV ENGINE_GRPC_ADDR=engine:50051
ENV QUERY_SERVER_PORT=50052
EXPOSE 50052
# Dependency-free TCP-connect health check (grpcio-health-checking is not a current
# agents/uv.lock dependency -- see architecture-discovery.md's health-check decision):
# confirms query.server's grpc.Server has bound and is accepting connections on its port.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD python3 -c "import socket; socket.create_connection(('127.0.0.1', 50052), timeout=2)" || exit 1
ENTRYPOINT ["python3", "-m", "query.server"]
