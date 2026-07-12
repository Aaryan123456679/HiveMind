# Plan -- subtask 6.2.2

1. `deploy/nginx.conf` -- reverse-proxy config for the `ui` service's nginx front-end:
   - `location /health` -> `proxy_pass http://api:8080/health;`
   - `location /query` -> `if ($request_method = GET) { rewrite ^ /index.html break; }`
     then `proxy_pass http://api:8080;` for POST.
   - `location /` -> `try_files $uri /index.html;` (SPA catch-all: `/`, `/ingest`, `/graph`,
     `/files`, `/admin`, and static assets under it).

2. `deploy/ui.Dockerfile` -- multi-stage:
   - Stage 1 (`node:20-alpine`): `WORKDIR /src`, `COPY ui/package.json ui/package-lock.json
     ./`, `RUN npm ci`, `COPY ui/ ./`, `RUN npm run build` (runs `tsc -b && vite build`,
     output `ui/dist`).
   - Stage 2 (`nginx:1.27-alpine`): `COPY --from=builder /src/dist
     /usr/share/nginx/html`, `COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf`,
     `EXPOSE 80`, wget-based HEALTHCHECK against `/` (nginx doesn't ship curl either, matches
     the wget precedent set by api.Dockerfile).
   - Build context: repo root (consistent with the other three Dockerfiles), e.g.
     `docker build -f deploy/ui.Dockerfile -t hivemind-ui .`

3. `deploy/.env.example` -- template listing override variable NAMES only:
   `LLM_PROVIDER=ollama`, `# OPENROUTER_API_KEY=`, `# GEMINI_API_KEY=` (commented out, no
   values), with a header comment explaining `cp deploy/.env.example deploy/.env` usage.

4. `deploy/docker-compose.yml`:
   - `services: engine, api, agents, ui`, one shared network `hivemind`.
   - `engine`: build `context: ..`, `dockerfile: deploy/engine.Dockerfile`; healthcheck
     inherited from image (no override needed, but explicit `healthcheck:` block added for
     clarity + a slightly faster interval for local dev feedback is unnecessary -- keep
     inherited).
   - `api`: build via `deploy/api.Dockerfile`; `depends_on: engine: condition:
     service_healthy`; `ports: ["8080:8080"]` (host-exposed, so the smoke-test script and a
     human demo user can `curl localhost:8080/health` directly, in addition to the
     ui-internal nginx proxy path).
   - `agents`: build via `deploy/agents.Dockerfile`; `depends_on: engine: condition:
     service_healthy`; `environment: LLM_PROVIDER: ${LLM_PROVIDER:-ollama}` (plus commented
     optional `OPENROUTER_API_KEY`/`GEMINI_API_KEY` passthrough via `${VAR:-}`, harmless if
     unset).
   - `ui`: build via `deploy/ui.Dockerfile`; `depends_on: api: condition: service_healthy`;
     `ports: ["8081:80"]` (avoid colliding with a host process already on 80; matches "host
     port distinct from container's internal 80" convention used by api's 8080:8080).
   - NOTE: `api` gateway does not currently dial `agents` (api/main.go's `QUERY_PIPELINE_ADDR`
     env var is unset by default -- see api/main.go's `queryPipeline()` doc comment,
     confirmed in architecture-discovery.md's earlier subtask 6.2.1 context / api/main.go
     read in this run). Wiring `QUERY_PIPELINE_ADDR=agents:50052` into the compose `api`
     service's environment is a natural, low-risk extra: it is what makes the `agents`
     service in this compose file actually reachable end-to-end from `api`'s `/query` route,
     matching this subtask's "engine + api + agents + ui" framing (agents would otherwise be
     built/started/healthchecked but structurally unused by the rest of the stack). Set it.

5. `deploy/smoke-test.sh` -- bash script:
   - `docker compose -f deploy/docker-compose.yml up -d --build`
   - poll (bounded retry loop, e.g. 60s budget) until `docker compose ps` reports all four
     services healthy, or fail loudly.
   - `curl -fsS http://localhost:8080/health` (api health route) -- assert response body.
   - `curl -fsS http://localhost:8081/` (ui root page via nginx) -- assert HTTP 200 and that
     the response body contains `<div id="root">` (confirms real built `index.html`, not an
     nginx default page).
   - on success, print a clear PASS summary; on any failure, dump `docker compose logs` for
     diagnosis before exiting non-zero.
   - always tear down (`docker compose down -v`) in a trap/finally, whether pass or fail.

6. Actually run: `docker compose up -d --build` manually first (sanity), inspect
   `docker compose ps`, then run `deploy/smoke-test.sh` for a real pass, then
   `docker compose down`.

7. self-consistency.json documenting the real run's output.

8. One local commit, Problem/Solution/Impact style matching `git log` convention.

9. handoff.json with pointers only.
