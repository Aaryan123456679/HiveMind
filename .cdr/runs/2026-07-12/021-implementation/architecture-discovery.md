# Architecture Discovery — 6.2.1 (deploy/ Dockerfiles)

## Docs
- `docs/HLD.md:85` lists `deploy/ # Dockerfiles/compose, CI (not yet built)`. Line 92: pipeline
  ordering ends "... -> demo/deploy -> ...". No existing LLD doc for `deploy/` (none of
  `docs/LLD/*.md` cover it — greenfield).
- `deploy/README.md` (pre-existing, only file in `deploy/` before this change): "Dockerfiles/
  compose for the Go API+engine and Python agent service, plus CI config. Target: a single
  small cloud VM/container service (single-node scope matches the storage engine's concurrency
  design — no multi-node orchestration needed)."
- Issue #31 subtask 6.2.2 (compose wiring, out of scope for this subtask) test spec explicitly
  says "curl api/ health route" — confirms an HTTP `/health` route on `api/` is an expected,
  intentional prerequisite of this subtask, not scope creep.

## `go.work`
```
go 1.26.4
use (
    ./api
    ./engine
)
```
`engine` and `api` are **separate Go modules** (`engine/go.mod`: module
`github.com/Aaryan123456679/HiveMind/engine`; `api/go.mod`: module
`github.com/Aaryan123456679/HiveMind/api`), joined only via `go.work` for local multi-module
development. Each therefore needs its own Docker build context / Dockerfile build stage — they
cannot share one `go build ./...` invocation inside a single module context. `api` depends on
`engine/rpc/gen` (imports `github.com/Aaryan123456679/HiveMind/engine/rpc/gen` — see
`api/main.go`), so `api`'s Docker build context must include both `api/` and `engine/`
directories (repo root as context, not just `api/`), and its Go module resolution needs either
a `go.work`-aware build or a `replace` directive. Simplest correct approach: build context =
repo root, `COPY . .`, run `go build` from `api/` using `go.work` (present at repo root) so the
module graph resolves `engine/rpc/gen` via the local workspace, not a network fetch.

## `engine/` binary entrypoint
- Only one `main` package exists under `engine`: `engine/cmd/smokeserver/main.go`. It opens
  real (not faked) `engine/catalog`, `engine/graph`, `engine/btree`, `engine/wal` storage
  rooted at a `-root` directory, wires `engine/rpc.NewServer`, registers it on a real
  `grpc.NewServer()`, listens on `-addr` (flag, default `127.0.0.1:0` — ephemeral), and prints
  `LISTENING <host:port>` to stdout before blocking on RPCs until SIGINT/SIGTERM.
- This is a smoke-test helper by original intent (used by `engine/rpc/integration_test.go` and
  `agents/ingestion/test_e2e_smoke.py`), but it is also the *only* buildable engine server
  binary in the repo today — there is no separate "production" `cmd/server`. For this Docker
  subtask it is the correct (only) entrypoint to containerize; `-addr 0.0.0.0:50051 -root
  /data` makes it listen on all interfaces inside the container instead of loopback-only.
- No HTTP surface; gRPC only. No `grpc_health_v1`/reflection currently registered anywhere in
  `engine/rpc` (`grep -rn "health" engine --include=*.go` matches only an unrelated B-tree
  comment ("tree-health checks") in `engine/btree/delete_test.go` — no real health RPC exists).

## `api/` binary entrypoint
- `api/main.go`'s `main()`: builds a `routes.QueryPipeline` (real gRPC client if
  `QUERY_PIPELINE_ADDR` is set, else a stub), registers routes via
  `routes.RegisterRoutes(mux, pipeline)`, and serves HTTP on `:$PORT` (default `8080`,
  `api/main.go`'s `port()` helper).
- `api/routes/query.go`'s `RegisterRoutes` (line 94) registers exactly one route: `POST
  /query`. **No `/health` route exists.** Confirmed via `grep -rn "health"
  api --include=*.go` (no matches).

## `agents/` service
- `agents/pyproject.toml`: `requires-python = ">=3.11"`; local `.venv` (per
  `agents/.venv/pyvenv.cfg`) is built against Python 3.11.7 (`/opt/anaconda3/bin/python3.11`).
  Matches Docker base `python:3.11-slim`.
- `agents/uv.lock` pins `grpcio==1.82.1`, `grpcio-tools==1.81.1`; `pyproject.toml` pins
  `protobuf>=6.33.5,<7.0.0` (comment: must stay >= the gencode baked into committed
  `hivemind_pb2*.py` stubs, currently 6.33.5, to avoid `VersionError` at import time — the
  same version-matching constraint noted in prior benchmark work). No system-level compiler
  deps are required at *runtime* (protobuf/grpcio ship prebuilt wheels for `python:3.11-slim`
  on standard `manylinux`/`linux/amd64`); `grpcio-tools` is a build-time codegen tool only
  used to regenerate stubs, not needed inside the runtime image.
- `agents/query/server.py` is the one Python long-running *service* process in this repo (a
  real `grpc.Server`, `QueryRunQueryServicer`, implementing the `RunQuery` RPC — the Python
  side of the `api/` <-> `agents/` gRPC boundary per GitHub issue #56). It must be invoked as
  `python -m query.server` from inside `agents/` (relative imports `from query.wiring import
  ...` require `agents/` on `sys.path`/cwd so the `query` package resolves) — not as a bare
  script path. Env vars: `ENGINE_GRPC_ADDR` (default `localhost:50051`), `QUERY_SERVER_PORT`
  (default `50052`), `LLM_PROVIDER` (consumed by `llm.factory.create_llm_client()`, required
  at `serve()`-time for a fully working process but not for the container merely starting).
  No HTTP surface; gRPC only. No health RPC registered (`grpcio-health-checking` is not a
  current dependency of `agents/pyproject.toml`/`uv.lock`).
- `agents/ingestion/` is a library invoked by other processes (no standalone service
  entrypoint found) — not part of this subtask's 3 named services (engine, api, agents); the
  agents Dockerfile packages the whole `agents/` codebase but its containerized *service*
  process is `query.server`.

## Health-check design decision (documented explicitly, not silently skipped)
- `api/` already speaks HTTP, and issue #31's own subtask 6.2.2 test spec references "curl
  api/ health route" as an already-expected feature — adding one trivial `GET /health` handler
  (200 `OK` text body, no dependencies) to `api/routes` is in-scope for this subtask because
  it is required to satisfy 6.2.1's own acceptance criterion ("container ... responds to a
  basic health check") for the one service where a curl-based check is natural.
- `engine/` and `agents/` (`query.server`) are gRPC-only processes with no HTTP endpoint.
  Registering a full `grpc_health_v1` service on both would require either a new Go health
  package import (available, since `google.golang.org/grpc/health` ships inside the already-
  required `google.golang.org/grpc` module — confirmed present in the local module cache,
  `~/go/pkg/mod/google.golang.org/grpc@v1.82.0/health/`) *and* a new `grpcio-health-checking`
  Python dependency (not currently pinned in `uv.lock`, and this environment cannot resolve/
  add a new PyPI dependency to the lockfile without a real `uv lock` network run) — extending
  the RPC surface itself is a larger change than "write a Dockerfile" and risks scope creep
  well past this subtask's named impacted modules (3 Dockerfiles only). Decision: for `engine`
  and `agents`, the Docker `HEALTHCHECK` is a dependency-free **TCP-connect script**
  (Go's `nc`/a one-line Python `socket.create_connection` check respectively) confirming the
  process is listening and accepting connections on its gRPC port — this satisfies the
  acceptance criterion's literal wording ("curl **or script**") without adding new Go/Python
  dependencies or extending `proto/hivemind.proto`'s RPC surface, which is out of scope here.
