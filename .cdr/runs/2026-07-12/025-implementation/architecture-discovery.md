# Architecture Discovery -- subtask 6.2.3

## Existing artifacts to translate (source of truth: deploy/docker-compose.yml)
- `engine`: no ports published externally in compose (only reachable inside the compose
  network at `engine:50051`); gRPC-only; `-root /data` local ephemeral storage;
  HEALTHCHECK = TCP connect on 50051.
- `api`: depends_on engine (service_healthy); env `QUERY_PIPELINE_ADDR=agents:50052`; port
  8080 published to host; HEALTHCHECK = `wget http://127.0.0.1:8080/health`.
- `agents`: depends_on engine (service_healthy); env `LLM_PROVIDER` (default `ollama`),
  `OPENROUTER_API_KEY`, `GEMINI_API_KEY` (both optional/blank-default); no published port
  (internal 50052 only); HEALTHCHECK = TCP connect on 50052.
- `ui`: depends_on api (service_healthy); port 8081->80 published; nginx reverse-proxies
  `/health` and `/query` to `api:8080` (deploy/nginx.conf, baked into image, no runtime
  config needed); HEALTHCHECK = `wget http://127.0.0.1:80/`.
- Single bridge network `hivemind` (Compose default DNS: service name -> container).

## Kubernetes translation decisions
1. **Namespace**: single `hivemind` namespace (`deploy/k8s/00-namespace.yaml`) to keep the
   demo self-contained and easy to `kubectl delete namespace hivemind` for full teardown --
   direct analogue of compose's single bridge network + `docker compose down -v`.
2. **Service discovery**: Kubernetes' own in-cluster DNS (`<service>.<namespace>.svc.cluster.
   local`, or short name `<service>` within the same namespace) is a drop-in replacement for
   Compose's embedded DNS -- `engine`, `api`, `agents` Service names are kept **identical** to
   the compose service names so no env var values need to change (e.g.
   `QUERY_PIPELINE_ADDR=agents:50052`, `ENGINE_GRPC_ADDR=engine:50051` keep working
   unmodified since Kubernetes resolves bare short names within the same namespace).
3. **depends_on / service_healthy has no direct k8s equivalent.** Kubernetes has no
   cross-Pod "wait for dependency to be healthy before starting" primitive (that's what
   Deployment readiness + client-side retry is for). Translation:
   - Each container gets a `readinessProbe` (matching compose's own HEALTHCHECK exec/port
     logic) so `kubectl get pods` / Service endpoints only route traffic to a genuinely-ready
     pod -- this replaces "don't accept traffic until healthy" but not "don't start until the
     dependency is healthy."
   - `api`, `agents`, and `ui`'s own code already tolerates a not-yet-ready dependency via
     gRPC client-side connection/retry semantics (confirmed: none of these processes crash-
     exit if `engine`/`agents` isn't reachable yet at process start -- server.py's crash-exit
     path noted in agents.Dockerfile's header is specifically for a *missing LLM_PROVIDER
     config*, not an unreachable engine; api/main.go dials its gRPC clients lazily per
     request, not at startup). This means Kubernetes' lack of a startup-order guarantee is
     safe here -- a `restartPolicy: Always` covers any transient early-connection failure,
     matching what would happen on a real multi-node deploy anyway (no assumption of a
     single compose-managed startup order should exist in production).
   - No initContainers added for this reason -- would be over-engineering not asked for and
     not needed given the above.
4. **HEALTHCHECK -> liveness+readiness probes**:
   - `engine`, `agents`: `exec` probe reusing the *exact* dependency-free TCP-connect check
     already baked into each image (`nc -z 127.0.0.1 50051`, the agents' Python one-liner)
     -- zero new tooling, exactly mirrors the Dockerfile HEALTHCHECK CMD.
   - `api`, `ui`: `httpGet` probe against `/health` and `/` respectively (kubelet has native
     HTTP probe support, no wget/curl exec needed -- strictly simpler than the Dockerfile's
     own wget-based HEALTHCHECK, same semantics).
   - Same timing (`periodSeconds`, `timeoutSeconds`, `failureThreshold`) roughly matches each
     Dockerfile's `--interval=10s --timeout=3s --retries=3`, `initialDelaySeconds` covers
     `--start-period`.
5. **ConfigMap vs Secret**: `LLM_PROVIDER` (non-sensitive, has a safe demo default) goes in a
   ConfigMap (`deploy/k8s/agents-config.yaml`), mirroring `deploy/.env.example`'s intent.
   `OPENROUTER_API_KEY`/`GEMINI_API_KEY` go in a **Secret template**
   (`deploy/k8s/agents-secret.example.yaml`, `stringData` placeholders only, `.example.yaml`
   naming + a comment so nobody applies it with real keys committed) -- never populated with
   real values in this repo, consistent with `deploy/.env.example`'s own "names only" rule
   and the repo's `.gitignore` `.env` carve-out precedent.
6. **Persistence**: `engine`'s compose config uses an anonymous/ephemeral `/data` root (no
   named volume in docker-compose.yml -- confirmed by re-reading it, no `volumes:` block
   exists for `engine`). Kept as ephemeral (`emptyDir`) in k8s too -- **no new persistence
   guarantee is being introduced or promised beyond what compose already provided**;
   anything more (PVC/StatefulSet) would be scope creep for a subtask whose stated goal is
   "translate compose's intent," not "add durability engine didn't have."
7. **Service types**: `engine` and `agents` get `ClusterIP` (internal-only, matches compose's
   unpublished ports). `api` and `ui` get `ClusterIP` too by default for the kind-validation
   path (reached via `kubectl port-forward`, exactly as instructed) -- but `ui`'s Service is
   also documented as the NodePort/LoadBalancer candidate for the eventual OCI path (single
   VM, so NodePort is the natural free-tier choice, no cloud LB needed/available).
8. **Manifest layout**: one YAML file per resource-group per service
   (`deploy/k8s/{service}-deployment.yaml` containing both Deployment+Service via `---`
   separator, matching the user's suggested naming), plus
   `deploy/k8s/00-namespace.yaml`, `deploy/k8s/agents-config.yaml`,
   `deploy/k8s/agents-secret.example.yaml`. Plain manifests (no Helm/kustomize) per user's
   stated preference -- 4 small services, no templating need that would justify the added
   complexity.
9. **Local validation tool**: `kind` was not present in this sandbox; installed via
   `brew install kind` (already had Docker Desktop + `kubectl` present). `k3d` was also
   absent; `kind` chosen since it was successfully installed and Docker Desktop is the
   backing container runtime already in use by 6.2.1/6.2.2's compose validation -- no need
   to also install k3d once kind works.
10. **Image loading for kind**: kind's Docker-in-Docker node containers do not share the
    host's Docker image cache by default; `kind load docker-image` is used after building all
    four images locally with the exact same `docker build -f deploy/X.Dockerfile -t
    hivemind-X:local ..` invocations 6.2.1/6.2.2 already validated (build context = repo
    root, unchanged) -- this is a k8s-loading step only, NOT an architecture change; the
    images themselves are byte-identical build recipes to 6.2.1/6.2.2's Dockerfiles
    (untouched in this subtask).
11. **OCI script scope**: `deploy/oci/provision.sh` is a plain bash script (not Terraform) --
    OCI's official free-tier ARM path is most commonly driven via the `oci` CLI, and a
    Terraform state file/backend would be one more unverifiable-in-this-sandbox artifact
    for no added correctness benefit at this scale (single VM, one-time setup). Script is
    explicitly commented as **unverified against live OCI** (no credentials available here)
    and gated behind an interactive confirmation prompt plus `set -n`-friendly structure so a
    user can `bash -n` / read-through before running for real.

## No new architecture components
Confirmed: no message broker, no queue, no new services. Only Kubernetes-native
Deployment/Service/ConfigMap/Secret/Namespace objects wrapping the exact same 4 existing
container images and their existing env-var contract. engine/api/agents/ui's gRPC/HTTP
topology is unchanged.
