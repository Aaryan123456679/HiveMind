# deploy/

Dockerfiles/compose for the Go API+engine and Python agent service, plus CI
config. Target: a single small cloud VM/container service (single-node
scope matches the storage engine's concurrency design — no multi-node
orchestration needed).

## Deploy paths

| Path | Files | Status |
|---|---|---|
| Local Docker Compose demo | `docker-compose.yml`, `*.Dockerfile`, `nginx.conf`, `.env.example`, `smoke-test.sh` | Working, verified (issue #31 subtasks 6.2.1/6.2.2 — see `.cdr/runs/2026-07-12/024-verification/`) |
| Kubernetes manifests | `k8s/` | Working, locally validated against a real `kind` cluster (issue #31 subtask 6.2.3 — see `k8s/README.md`) |
| OCI Always-Free ARM + k3s | `oci/` | Documented/scripted, **unverified against a live OCI account** (no credentials available in the sandbox that built it — review before use, see `oci/README.md`) |

`deploy/` is no longer a placeholder (see `docs/HLD.md`'s repo-layout section) — all three
paths above target the exact same 4 service images (engine/api/agents/ui), just different
orchestration layers on top of them. No message queue or other new architecture component
was introduced by any of these paths; engine/api/agents/ui remain gRPC/HTTP-only.
