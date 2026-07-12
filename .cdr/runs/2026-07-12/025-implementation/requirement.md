# Requirement -- subtask 6.2.3 "Deploy target host" (GitHub issue #31)

## Context
Subtask 6.2.1 (`.cdr/runs/2026-07-12/021-implementation`) produced the four service
Dockerfiles (`deploy/engine.Dockerfile`, `deploy/api.Dockerfile`, `deploy/agents.Dockerfile`,
implicit ui build). Subtask 6.2.2 (`.cdr/runs/2026-07-12/023-implementation`) produced
`deploy/docker-compose.yml`, `deploy/ui.Dockerfile`, `deploy/nginx.conf`, `deploy/.env.example`,
`deploy/smoke-test.sh` -- a working local Docker Compose demo stack, verified PASS in
`.cdr/runs/2026-07-12/024-verification`.

6.2.3 was previously blocked on an open question, OQ-3: "what hosting target should the real
deploy artifact target?" That question was resolved directly with the user this session
(not re-litigated here):

- Target platform: **Kubernetes**, intended to eventually run on **Oracle Cloud
  Infrastructure (OCI) Always-Free-tier compute** (a small ARM VM or VMs running k3s).
- **Explicitly out of scope**: no Kafka or any message queue -- this is deploy-config-only,
  no new architecture components. engine/api/agents/ui stay exactly as built (gRPC/HTTP,
  no queue).
- **Explicitly out of scope**: no real OCI provisioning in this sandbox -- no OCI credentials
  exist here. This subtask produces artifacts only (manifests + scripts + docs) for a human
  with their own OCI account to run later.

## Acceptance criteria (this subtask)
1. Plain Kubernetes manifests under `deploy/k8s/` for all four services, translating
   `deploy/docker-compose.yml`'s existing wiring (env vars, network topology, health-check
   semantics) into Deployments/Services/ConfigMaps -- not an architecture rewrite.
2. A REAL local-cluster validation: stand up a local kind/k3d cluster in this sandbox,
   `kubectl apply -f deploy/k8s/`, confirm all 4 pods reach Ready, and curl the same
   `/health` and `/` endpoints 6.2.2's smoke test checked (via port-forward or NodePort/
   Service), executed for real -- not just written and assumed correct.
3. An OCI provisioning script (`deploy/oci/provision.sh`) documenting/automating the steps a
   user WOULD run with their own OCI CLI configured (Always-Free ARM compute instance
   creation, k3s install, applying `deploy/k8s/` manifests). Explicitly marked as unverified
   against a live OCI account (none available in this sandbox) -- dry-run/review required
   before use.
4. `deploy/k8s/README.md` (or folded into `deploy/README.md`) documenting prerequisites, the
   local kind/k3d validation path (the one actually executed), and the eventual OCI path.
5. `docs/HLD.md` deploy/ placeholder note updated to reflect deploy/ is no longer a
   placeholder.

## Explicitly out of scope
- Provisioning any real OCI resources (no credentials in this sandbox).
- Adding a message queue / Kafka / any new architecture component.
- Implementing api's `/graph`, `/files`, `/admin` handlers (separate, pre-existing scope gap
  noted in 6.2.2's handoff, unrelated to this subtask).
- TLS/ingress/cert-manager, autoscaling, multi-node HA -- single small OCI free-tier VM is
  the stated target; a single-node kind cluster is sufficient to validate manifest
  correctness.
- Helm chart -- plain manifests chosen per user's explicit preference ("prefer that unless
  you have good reason otherwise"); no reason to deviate found (4 small services, no
  templating complexity that would justify Helm).

## I4 note
This agent (implementation) does not verify its own work per invariant I4. The
self-consistency step below is an internal sanity check only (build/apply green, matrix
covered) -- it is NOT the verification gate. A separate `/cdr:verify` run is required before
this subtask is considered done.
