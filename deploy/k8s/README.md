# deploy/k8s/ -- Kubernetes manifests

GitHub issue #31 subtask 6.2.3. Plain Kubernetes manifests (no Helm/kustomize -- 4 small
services, no templating complexity that would justify it) translating
`deploy/docker-compose.yml`'s existing service wiring 1:1 into Deployments/Services/
ConfigMap/Secret-template. See this run's `architecture-discovery.md`
(`.cdr/runs/2026-07-12/025-implementation/architecture-discovery.md`) for the full
compose-to-k8s translation reasoning (service-name-as-DNS, probe design, why no
depends_on-equivalent is needed, etc).

Target platform: Kubernetes, intended to eventually run on a small Oracle Cloud
Infrastructure (OCI) Always-Free-tier ARM VM running k3s (see `deploy/oci/README.md` for
that path). This directory's manifests are plain upstream Kubernetes YAML -- no k3s-specific
extensions used -- so they also work unmodified against any other Kubernetes cluster
(kind, k3d, GKE/EKS/AKS, etc), which is what makes the local validation below meaningful.

## Files
| File | Contents |
|---|---|
| `00-namespace.yaml` | `hivemind` namespace (all resources live here) |
| `engine-deployment.yaml` | engine Deployment + Service (ClusterIP, gRPC :50051) |
| `api-deployment.yaml` | api Deployment + Service (ClusterIP, HTTP :8080) |
| `agents-deployment.yaml` | agents Deployment + Service (ClusterIP, gRPC :50052) |
| `agents-config.yaml` | ConfigMap: `LLM_PROVIDER` etc (non-sensitive) |
| `agents-secret.example.yaml` | Secret **template** for `OPENROUTER_API_KEY`/`GEMINI_API_KEY` -- names only, no real values. `kubectl apply -f deploy/k8s/` applies this file's empty placeholders as-is (matching compose's `${VAR:-}` optional-blank-default behavior); copy it to `agents-secret.yaml`, fill in real values, and re-apply if you need a real hosted LLM provider. `agents-secret.yaml` (non-`.example`) is `.gitignore`d -- never commit real keys. |
| `ui-deployment.yaml` | ui Deployment + Service (ClusterIP; nginx config baked into the image already, no runtime config needed) |

## Prerequisites
- `kubectl`
- A local cluster tool: `kind` (used for this subtask's validation) or `k3d`. Neither was
  present in the sandbox this was built in -- installed via `brew install kind`.
- Docker (to build the 4 service images and load them into the local cluster).

## Local validation (kind) -- actually executed for this subtask

This is the exact sequence run in this sandbox; every step below produced real output
(captured in `.cdr/runs/2026-07-12/025-implementation/self-consistency.json`), not just
written and assumed correct.

```bash
# 1. Build the 4 images (same Dockerfiles/build-context as deploy/docker-compose.yml)
docker build -f deploy/engine.Dockerfile -t hivemind-engine:kindtest .
docker build -f deploy/api.Dockerfile    -t hivemind-api:kindtest .
docker build -f deploy/agents.Dockerfile -t hivemind-agents:kindtest .
docker build -f deploy/ui.Dockerfile     -t hivemind-ui:kindtest .

# 2. Create a local kind cluster
kind create cluster --name hivemind-6-2-3

# 3. Load the images into the kind cluster (kind's nodes don't share the host's image cache)
kind load docker-image hivemind-engine:kindtest --name hivemind-6-2-3
kind load docker-image hivemind-api:kindtest    --name hivemind-6-2-3
kind load docker-image hivemind-agents:kindtest --name hivemind-6-2-3
kind load docker-image hivemind-ui:kindtest     --name hivemind-6-2-3

# 4. Apply all manifests
kubectl apply -f deploy/k8s/

# 5. Wait for all 4 pods Ready
kubectl wait --for=condition=Ready pod --all -n hivemind --timeout=90s
kubectl get pods -n hivemind
#   NAME                      READY   STATUS    RESTARTS   AGE
#   agents-79955ff954-4hg4x   1/1     Running   0          20s
#   api-7bbd5b67f-4mbvf       1/1     Running   0          20s
#   engine-5fd6bd4dcd-bff75   1/1     Running   0          20s
#   ui-7f8797d66-9vt9g        1/1     Running   0          20s
# (this is real captured output from this subtask's run, not illustrative)

# 6. Confirm the same /health and / endpoints 6.2.2's smoke test checked, via port-forward
kubectl port-forward -n hivemind svc/api 18080:8080 &
kubectl port-forward -n hivemind svc/ui  18081:80   &
curl http://localhost:18080/health      # -> "ok"
curl http://localhost:18081/ | grep -o '<div id="root">'   # -> found (SPA shell served)

# 7. Tear down
kind delete cluster --name hivemind-6-2-3
```

All 4 pods reached `1/1 Ready` within a few seconds (no crash loops, no probe failures) and
both endpoints responded correctly through the cluster. In-namespace DNS was also confirmed
directly (`kubectl run --image=busybox -- nslookup engine|agents|api` from inside the
cluster all resolved to the expected ClusterIP Service addresses), confirming the
`ENGINE_GRPC_ADDR=engine:50051` / `QUERY_PIPELINE_ADDR=agents:50052` env values (unchanged
from `deploy/docker-compose.yml`) resolve correctly under Kubernetes' own DNS too.

## Eventual OCI path

Once you have your own OCI account/credentials configured, see `deploy/oci/README.md` and
`deploy/oci/provision.sh` -- that script is **unverified against a live account** (none
available in this sandbox) and requires review before running. It provisions an
Always-Free ARM compute instance, installs k3s, and applies these same `deploy/k8s/`
manifests unmodified (they're plain upstream k8s YAML, no kind-specific behavior).

Two things you'll need to adjust for a real cluster (called out in `deploy/oci/README.md`
too):
1. **Images**: the manifests reference local `:kindtest` tags. Push the 4 images to a
   registry the k3s node can pull from (or build directly on the ARM VM) and update each
   manifest's `image:`/`imagePullPolicy`.
2. **ui exposure**: change `ui-deployment.yaml`'s Service `type: ClusterIP` to
   `type: NodePort` (no cloud LoadBalancer on a single free-tier VM) if you want to reach the
   UI directly from outside the cluster instead of via `kubectl port-forward`/an SSH tunnel.

## Not in scope for this subtask
- TLS/ingress/cert-manager, autoscaling, multi-node HA -- a single small OCI free-tier VM is
  the stated target.
- A message queue / Kafka / any new architecture component -- explicitly confirmed out of
  scope; this is deploy-config-only.
- Actually provisioning OCI resources -- no credentials available in this sandbox.
