# Architecture discovery -- subtask 6.2.2

## Docs read
- docs/HLD.md:85 (`deploy/ # Dockerfiles/compose, CI (not yet built)`), :92 (pipeline
  ordering, deploy is last stage). No deploy-specific LLD file exists under docs/LLD/ (11
  LLD files present, none deploy-related) -- confirmed via directory listing.
- deploy/README.md (pre-existing, one line, unchanged): "Dockerfiles/compose for Go
  API+engine and Python agent service, plus CI config. Target: single small cloud VM/
  container service."

## Existing Dockerfiles (subtask 6.2.1, commit edb8d6e, verified run 022)
- deploy/engine.Dockerfile: `ENTRYPOINT ["smokeserver", "-root", "/data", "-addr",
  "0.0.0.0:50051"]`, `EXPOSE 50051`, HEALTHCHECK = busybox `nc -z 127.0.0.1 50051`.
- deploy/api.Dockerfile: `ENV PORT=8080`, `EXPOSE 8080`, `ENTRYPOINT ["api"]`,
  HEALTHCHECK = `wget -q -O- http://127.0.0.1:8080/health`. Build context MUST be repo root.
  api/main.go:91-94 -- PORT env var read, default "8080".
- deploy/agents.Dockerfile: `ENV ENGINE_GRPC_ADDR=engine:50051 QUERY_SERVER_PORT=50052`
  already baked in (uses the literal hostname `engine`, which matches this compose file's
  service name -- no override needed as long as the compose service is named `engine`).
  `EXPOSE 50052`, HEALTHCHECK = python socket connect. REQUIRES `LLM_PROVIDER` env var to be
  set (else agents/query/server.py's serve() crashes before binding the gRPC port --
  documented in run 021's architecture-discovery.md and self-consistency.json).

## UI wire contract (why a reverse proxy is needed)
- ui/src/routes/QueryView.tsx:46 -- `fetch("/query", { method: "POST", ... })`, same-origin
  relative path, no base URL. Doc comment explicitly: "deploy/ reverse-proxy wiring does not
  exist yet" -- this subtask is what closes that gap.
- ui/src/routes/GraphView.tsx:64 -- `fetch(`/graph?path=...`)`. ui/src/routes/
  FilesAdminView.tsx:90,116 -- `fetch("/files", "GET")`, `fetch("/admin", "GET")`.
- ui/src/App.tsx -- client-side routes at exactly the same path strings (`/query`, `/graph`,
  `/files`, `/admin`, plus `/ingest`), driven by react-router-dom's `<BrowserRouter>` (see
  main.tsx) with a root `<Navigate to="/query" replace>` (client-side history change, not an
  HTTP request).
- api/routes/query.go:262-264 `RegisterRoutes` currently wires only `/query` (POST-only,
  405 otherwise -- see query.go:59-60) and `/health` (GET). `/graph`, `/files`, `/admin` have
  NO Go handlers yet (out of scope for this subtask, confirmed in requirement.md).

## Disclosed design decision: path collision between SPA client routes and API paths
`/query` is both a react-router client-side route (rendered on GET page-load/refresh) and a
real API endpoint (POST only, api/routes/query.go). A single nginx `location /query` block
cannot use two different `proxy_pass` targets by path alone. Resolution: use nginx's
documented `if` inside a `location` block (restricted to `return`/`rewrite ... break`, one of
the accepted safe uses per nginx's "IfIsEvil" guidance) to special-case GET on that path back
to `index.html` (letting the SPA render the /query view client-side, exactly like a hard
refresh on that URL should behave) while every other method (POST, from the app's own fetch
call) is proxied to the `api` service.

`/graph`, `/files`, `/admin` are NOT given their own proxy location in this subtask: api has
no handlers for them yet (confirmed above), so proxying would only ever return 404 from api
instead of the SPA -- worse for the demo/local-run experience than falling through to the
existing SPA catch-all (`location / { try_files $uri /index.html; }`), which already serves
those routes today via ui/'s own client-side router. This is consistent with the
requirement's explicit "out of scope" list (implementing api's /graph, /files, /admin Go
handlers is subtask work not assigned here) and does not regress anything -- those fetch
calls already 404 today with no reverse proxy at all. `/health` gets its own dedicated
proxy location since it has no client-route counterpart and is exactly the acceptance
criterion's smoke-check target.

## No existing ui Dockerfile
`ls deploy/` (post-6.2.1) contains only agents.Dockerfile, api.Dockerfile, engine.Dockerfile,
README.md -- no ui Dockerfile exists; this subtask must add one.

## Compose network / dependency plan
- Docker Compose's default bridge network + built-in DNS resolves service names
  (`engine`, `api`, `agents`, `ui`) as hostnames automatically -- no explicit `networks:`
  section is strictly required, but one named network (`hivemind`) is declared explicitly
  for clarity/robustness rather than relying on the implicit default-project network.
- `depends_on` with `condition: service_healthy` (Compose v2, confirmed installed:
  `Docker Compose version v2.34.0-desktop.1`) is used so `api`/`agents` wait for `engine`'s
  HEALTHCHECK, and `ui` waits for `api`'s HEALTHCHECK, before Compose considers them
  startable -- avoids a flaky first-few-seconds race during `docker compose up`.
- `agents` container gets `LLM_PROVIDER` defaulted to `ollama` via compose variable
  substitution syntax `${LLM_PROVIDER:-ollama}` inside the compose file's `environment:`
  block (demo default, no real API key values referenced or hardcoded). Compose
  automatically reads a `.env` file located next to the compose file (`deploy/.env`, if
  present) for this substitution -- no explicit `env_file:` directive is used, since that
  form errors out if the referenced file doesn't exist, whereas plain `${VAR:-default}`
  substitution degrades gracefully to the default when no `deploy/.env` is present. A new
  `deploy/.env.example` template documents the override variable NAMES only (never real
  values: `LLM_PROVIDER`, `OPENROUTER_API_KEY`, `GEMINI_API_KEY`) for a user who wants to
  point the demo at a real provider instead of the ollama default. `deploy/.env` itself is
  already covered by the repo root .gitignore's blanket `.env` / `.env.*` rules, with a
  `!*.env.example` carve-out already in place (confirmed via `cat .gitignore`), so
  `deploy/.env.example` is safely committable and `deploy/.env` (if a user creates one) stays
  untracked.
