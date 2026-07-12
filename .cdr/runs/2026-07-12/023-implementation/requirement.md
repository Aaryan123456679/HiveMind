# Requirement -- GitHub issue #31, subtask 6.2.2

Title: Compose wiring — engine + api + agents + ui, local/demo run

## Acceptance criteria
A single compose command brings up all four services (engine, api, agents, ui) networked
together, and the UI can successfully reach the api/ gateway.

## Test spec
`docker compose up` followed by a scripted smoke check (curl api/ health route, load ui/ root
page) confirms all services are reachable.

## Impacted modules
- deploy/docker-compose.yml (new)

## Context carried over from subtask 6.2.1 (commit edb8d6e, verified in run 022)
- deploy/engine.Dockerfile: builds engine/cmd/smokeserver, gRPC on 0.0.0.0:50051, TCP-connect
  HEALTHCHECK (`nc -z 127.0.0.1 50051`). ENTRYPOINT hardcodes `-addr 0.0.0.0:50051`.
- deploy/api.Dockerfile: build context MUST be repo root (needs go.work to resolve
  engine/rpc/gen cross-module import). ENV PORT=8080, EXPOSE 8080, GET /health -> 200 "ok"
  (api/routes/query.go's HealthHandler, registered in RegisterRoutes). wget-based
  HEALTHCHECK. ENTRYPOINT ["api"], PORT read from env (api/main.go:91-94, default "8080" if
  unset).
- deploy/agents.Dockerfile: python:3.11-slim, agents/query/server.py gRPC service.
  ENV ENGINE_GRPC_ADDR=engine:50051 QUERY_SERVER_PORT=50052 baked into the Dockerfile already
  (compose can rely on the service name `engine` resolving via Docker's embedded DNS on the
  shared compose network). REQUIRES LLM_PROVIDER env var (e.g. "ollama") to be set at
  `docker run`/compose time, else agents/query/server.py's serve() constructs an LLM client
  before binding the gRPC port and crashes before the healthcheck can ever pass. This
  subtask is demo/local wiring only (not the real deploy-target subtask 6.2.3, separately
  blocked on an open question) so LLM_PROVIDER defaults to "ollama" in compose environment,
  no real API key values referenced or hardcoded.
- ui/: Vite/React SPA (ui/package.json). ui/src/routes/{QueryView,GraphView,
  FilesAdminView}.tsx call same-origin relative fetch paths ("/query", "/graph", "/files",
  "/admin") deliberately, per QueryView.tsx's own doc comment: "deploy/ reverse-proxy wiring
  does not exist yet" -- this subtask is what closes that gap. No existing ui Dockerfile.

## Out of scope
- Implementing api/'s /graph, /files, /admin Go handlers (routes.RegisterRoutes only wires
  /query and /health today) -- reverse-proxy can forward those paths to api regardless of
  whether api has real handlers for them; a 404 from api is still "the UI reached the api/
  gateway", which is this subtask's acceptance bar, not "all routes fully functional".
- subtask 6.2.3 (real deploy target), separately blocked on an open question.
- Adding grpc_health_v1 health service to engine/agents (documented decision from 6.2.1,
  unchanged).
