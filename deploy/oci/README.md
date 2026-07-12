# deploy/oci/ -- Oracle Cloud Infrastructure Always-Free deploy path

GitHub issue #31 subtask 6.2.3. This directory documents/automates the steps a user **with
their own OCI account and configured `oci` CLI** would run to host HiveMind on an
Always-Free-tier ARM compute instance running k3s.

**Status: unverified against a live OCI account.** No OCI credentials exist in the sandbox
this was written in, so `deploy/oci/provision.sh` has been reviewed and syntax-checked
(`bash -n`) but never executed end-to-end against a real account. Read it fully, understand
every `oci`/`ssh`/`k3s` call, and dry-run/review before trusting it. This is the resolved
scope for this subtask (see this run's requirement.md) -- provisioning real cloud resources
was explicitly out of scope for the implementation agent to attempt.

## What it does
1. Launches a `VM.Standard.A1.Flex` (Always-Free ARM shape) compute instance in your
   compartment/subnet.
2. Waits for SSH, installs k3s (single-binary Kubernetes, right-sized for one small VM --
   see `deploy/k8s/README.md` for why plain k3s rather than a managed/multi-node cluster).
3. Copies `deploy/k8s/` manifests to the instance and applies them with k3s's bundled
   `kubectl`.

## Prerequisites (on YOUR machine, not this repo's dev sandbox)
- `oci` CLI installed and configured (`oci setup config`) against a real account with
  Always-Free ARM compute available in your region.
- A compartment OCID, and an existing VCN + public subnet (OCI's Networking Quickstart in
  the console, or `oci network` commands, can create one -- intentionally not automated
  here; a network topology is a one-time account-level decision, not something this script
  should assume/override).
- An ARM (aarch64) base image OCID (Ubuntu or Oracle Linux ARM image from your tenancy's
  image list: `oci compute image list --compartment-id <id> --operating-system "Canonical
  Ubuntu" --shape "VM.Standard.A1.Flex"`).
- An SSH key pair.

## Usage
```bash
export OCI_COMPARTMENT_ID=ocid1.compartment.oc1..xxxx
export OCI_SUBNET_ID=ocid1.subnet.oc1..xxxx
export OCI_AVAILABILITY_DOMAIN=Uocm:PHX-AD-1
export OCI_IMAGE_ID=ocid1.image.oc1..xxxx
export SSH_PUBLIC_KEY_FILE=~/.ssh/id_ed25519.pub
export SSH_PRIVATE_KEY_FILE=~/.ssh/id_ed25519

# Review the script fully first.
bash -n deploy/oci/provision.sh
./deploy/oci/provision.sh
```

## Image availability gap (must resolve before applying manifests on the real VM)
`deploy/k8s/*-deployment.yaml` reference local `:kindtest`-tagged images (this subtask's
local kind-validation tag, built straight from `deploy/{engine,api,agents,ui}.Dockerfile`
with repo-root build context, unchanged from 6.2.1/6.2.2). A real k3s node has no access to
your local Docker image cache, so before/after running `provision.sh` you must either:
- push the four images to a registry (Docker Hub, GHCR, OCI's own Container Registry) the
  k3s node can pull, and update each manifest's `image:` + `imagePullPolicy`, or
- build them directly on the ARM VM (same Dockerfiles, ARM-native build -- OCI's free-tier
  shape is ARM, so no cross-compilation needed) and `sudo k3s ctr images import` a
  `docker save` tarball if you'd rather not stand up a registry for a single-VM demo.

This step is deliberately manual/reviewed, not automated in `provision.sh`, since registry
choice is account-specific.

## What was actually verified vs. not
- **Verified** (this subtask, in a local `kind` cluster -- see `deploy/k8s/README.md`): the
  manifests themselves are internally consistent -- correct image references, env vars,
  in-namespace Service DNS names, and health/readiness probes; all 4 pods reach Ready and
  `/health` + `/` respond correctly through `kubectl port-forward`.
- **Not verified** (no OCI account/credentials in this sandbox): the `oci` CLI calls in
  `provision.sh` succeeding against a real tenancy, k3s installing cleanly on real
  Always-Free ARM hardware, ARM-native image builds on the VM, and any OCI-specific network/
  security-list configuration (e.g. opening ports 80/443/6443 in the VCN's security list --
  not handled by this script; add explicitly before relying on NodePort access).
