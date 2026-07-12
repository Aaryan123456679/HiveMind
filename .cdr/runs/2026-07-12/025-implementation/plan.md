# Plan -- subtask 6.2.3

1. Write `deploy/k8s/00-namespace.yaml` (namespace `hivemind`).
2. Write per-service Deployment+Service manifests (`engine`, `api`, `agents`, `ui`),
   translating docker-compose.yml 1:1 per architecture-discovery.md decisions 1-7.
3. Write `deploy/k8s/agents-config.yaml` (ConfigMap: `LLM_PROVIDER`) and
   `deploy/k8s/agents-secret.example.yaml` (Secret template: `OPENROUTER_API_KEY`,
   `GEMINI_API_KEY` placeholders, clearly marked example-only).
4. Install `kind` (not present in sandbox) via `brew install kind`; confirm `kubectl`
   already present.
5. Create a kind cluster (`kind create cluster --name hivemind-6-2-3`).
6. Build all four service images locally (same Dockerfiles/build-context as 6.2.1/6.2.2,
   tagged `:kindtest`), `kind load docker-image` each into the cluster.
7. `kubectl apply -f deploy/k8s/` against the kind cluster; poll `kubectl get pods -n
   hivemind` until all 4 Ready.
8. Validate health endpoints for real:
   - `kubectl port-forward` api's Service -> curl `/health`.
   - `kubectl port-forward` ui's Service -> curl `/` and check for SPA markup (same
     assertion `deploy/smoke-test.sh` uses: `<div id="root">`).
9. Tear down the kind cluster (`kind delete cluster`) once validated -- don't leave a
   dangling cluster.
10. Write `deploy/oci/provision.sh` (+ short `deploy/oci/README.md`) -- documented,
    unverified-against-live-OCI, review-before-run.
11. Write `deploy/k8s/README.md` documenting prerequisites, the kind path just executed
    (with real commands/output), and the eventual OCI path.
12. Update `deploy/README.md` to point at the new k8s/OCI paths; update `docs/HLD.md`'s
    `deploy/` repo-layout line.
13. Self-consistency check (internal only, not verification): re-run the kind
    apply+curl sequence one more time end-to-end to confirm it's reproducible from a clean
    slate, capture output into self-consistency.json.
14. One local commit (Problem/Solution/Impact style, matching `git log` convention).
15. Write handoff.json (pointers only).

## Explicit non-goals for this plan
- No Helm/kustomize.
- No CI wiring (no k8s job in any pipeline config -- none exists yet, out of scope).
- No real `oci` CLI invocation.
- No TLS/ingress/cert-manager/HPA.
