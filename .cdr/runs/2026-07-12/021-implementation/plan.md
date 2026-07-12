# Plan — 6.2.1 Dockerfiles

## 1. `api/routes/query.go` — add `/health` route (small, justified addition)
- New `HealthHandler` (or inline `http.HandlerFunc`) returning `200 OK`, `Content-Type:
  text/plain`, body `ok`.
- `RegisterRoutes` gets one added line: `mux.HandleFunc("/health", HealthHandler)`.
- Decision explicitly documented in architecture-discovery.md: justified because (a) issue
  #31's own acceptance criteria for 6.2.1 requires "container starts and responds to a basic
  health check" and api/ is the one service where an HTTP curl check is natural, and (b)
  subtask 6.2.2's test spec already presumes this route exists ("curl api/ health route").
  No other behavior of `/query` touched.

## 2. `deploy/engine.Dockerfile`
- Multi-stage build, repo-root build context.
- Stage 1 (`builder`): `golang:1.26-alpine`, `WORKDIR /src`, `COPY go.work go.work.sum ./`,
  `COPY engine/ ./engine/`, `COPY api/ ./api/` (needed only so `go.work`'s workspace resolves
  without error — `go build` invoked with `-C engine` or `cd engine` so only the engine
  module's own binary is built), `RUN go build -o /out/smokeserver ./engine/cmd/smokeserver`.
- Stage 2 (runtime): `alpine:3.20` (small, includes busybox `nc` for the health check),
  `COPY --from=builder /out/smokeserver /usr/local/bin/smokeserver`, `RUN mkdir -p /data`,
  `ENV ENGINE_ADDR=0.0.0.0:50051`, `EXPOSE 50051`,
  `HEALTHCHECK CMD nc -z 127.0.0.1 50051 || exit 1`,
  `ENTRYPOINT ["smokeserver", "-root", "/data", "-addr", "0.0.0.0:50051"]`.

## 3. `deploy/api.Dockerfile`
- Multi-stage build, repo-root build context (needed for the cross-module `engine/rpc/gen`
  import — see architecture-discovery.md).
- Stage 1 (`builder`): `golang:1.26-alpine`, `WORKDIR /src`, `COPY go.work go.work.sum ./`,
  `COPY api/ ./api/`, `COPY engine/ ./engine/`, `RUN go build -o /out/api ./api`.
- Stage 2 (runtime): `alpine:3.20`, `COPY --from=builder /out/api /usr/local/bin/api`,
  `ENV PORT=8080`, `EXPOSE 8080`,
  `HEALTHCHECK CMD wget -q -O- http://127.0.0.1:8080/health || exit 1`,
  `ENTRYPOINT ["api"]`.
- (`wget` is present in alpine's busybox by default, no extra apk install needed — avoids
  pulling in a separate curl package purely for the HEALTHCHECK line; `docker run`'s own
  manual verification below still uses `curl` from the host side against the published port.)

## 4. `deploy/agents.Dockerfile`
- Single stage: `python:3.11-slim`, `WORKDIR /app`, `COPY agents/ ./agents/` (also copy
  `proto/` only if referenced — checked: not needed, `agents/hivemind_pb2*.py` stubs are
  already committed/generated, no protoc invocation needed at build or run time),
  `WORKDIR /app/agents`, `RUN pip install --no-cache-dir .` (installs from
  `pyproject.toml`'s `[project.dependencies]`, matching the version-pinned protobuf/grpcio
  constraints already enforced there),
  `ENV QUERY_SERVER_PORT=50052`, `EXPOSE 50052`,
  `HEALTHCHECK CMD python3 -c "import socket; socket.create_connection(('127.0.0.1', 50052), timeout=2)" || exit 1`,
  `ENTRYPOINT ["python3", "-m", "query.server"]`.
- Note (disclosed, not hidden): `query.server`'s `serve()` calls
  `llm.factory.create_llm_client()` which resolves `LLM_PROVIDER` from the environment; a
  bare `docker run` with no env config may fail at that step if the default provider requires
  an API key. The Docker-level acceptance criterion here is "container starts and responds to
  a basic health check" — self-consistency below will attempt an unconfigured `docker run`
  first and, if `create_llm_client()` needs env vars to proceed past that line, will re-run
  with a minimal provider config (or fall back to confirming via `docker run --entrypoint
  python3 ... -c "import query.server"` as an import-level smoke check) and disclose exactly
  which was actually exercised.

## 5. Validation
- `docker build -f deploy/engine.Dockerfile .`
- `docker build -f deploy/api.Dockerfile .`
- `docker build -f deploy/agents.Dockerfile .`
- `docker run` each + health check per validation-matrix.json.

## Explicitly out of scope for this commit
- `deploy/docker-compose.yml` (subtask 6.2.2).
- Any CI workflow wiring.
- Real grpc_health_v1 service registration in engine/agents (see health-check decision above).
