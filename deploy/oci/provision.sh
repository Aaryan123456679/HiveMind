#!/usr/bin/env bash
# deploy/oci/provision.sh -- GitHub issue #31 subtask 6.2.3.
#
# ============================================================================
# ** UNVERIFIED AGAINST A LIVE OCI ACCOUNT **
# This script was written and reviewed but NEVER executed against a real Oracle Cloud
# Infrastructure account -- no OCI credentials exist in the sandbox that produced it (see
# this run's requirement.md / architecture-discovery.md). Read every step, run with
# `bash -n deploy/oci/provision.sh` first (syntax-only check), and dry-run/review each `oci`
# CLI call against your own account/quotas before trusting it end-to-end. Treat it as a
# documented recipe, not a proven tool.
# ============================================================================
#
# What this does (when run by a human with their own OCI CLI configured):
#   1. Creates an Always-Free-tier ARM compute instance (VM.Standard.A1.Flex, the shape
#      OCI's Always Free tier offers -- up to 4 OCPUs / 24GB RAM total across instances at
#      time of writing; adjust OCPUS/MEMORY_GB below to fit your account's free allowance).
#   2. Waits for it to become reachable over SSH.
#   3. Installs k3s (a lightweight single-binary Kubernetes distribution well-suited to a
#      single small ARM VM -- this is the "eventually run on OCI Always-Free ARM VM(s)
#      running k3s" target confirmed with the user, see requirement.md).
#   4. Copies deploy/k8s/*.yaml to the instance and applies them with the k3s-bundled
#      `kubectl` (k3s ships its own kubectl-compatible binary at /usr/local/bin/kubectl).
#
# Prerequisites (all on the machine running this script, NOT on this sandbox):
#   - OCI CLI installed and configured (`oci setup config`) with a real account that has
#     Always-Free ARM compute available in your chosen region/availability domain.
#   - An OCI compartment OCID, a VCN + subnet already created (or use OCI's "Networking
#     Quickstart" -- out of scope for this script to also create networking from scratch;
#     see deploy/oci/README.md for pointers).
#   - An SSH key pair.
#   - `ssh`, `scp` available locally.
#
# Usage:
#   export OCI_COMPARTMENT_ID=ocid1.compartment.oc1..xxxx
#   export OCI_SUBNET_ID=ocid1.subnet.oc1..xxxx
#   export OCI_AVAILABILITY_DOMAIN=Uocm:PHX-AD-1
#   export OCI_IMAGE_ID=ocid1.image.oc1..xxxx   # an Ubuntu/Oracle Linux ARM (aarch64) image
#   export SSH_PUBLIC_KEY_FILE=~/.ssh/id_ed25519.pub
#   export SSH_PRIVATE_KEY_FILE=~/.ssh/id_ed25519
#   ./deploy/oci/provision.sh
#
# This script deliberately does NOT hardcode any compartment/subnet/image OCID -- those are
# account- and region-specific and must come from the user's own `oci` CLI setup.

set -euo pipefail

: "${OCI_COMPARTMENT_ID:?Set OCI_COMPARTMENT_ID to your OCI compartment OCID}"
: "${OCI_SUBNET_ID:?Set OCI_SUBNET_ID to your VCN subnet OCID}"
: "${OCI_AVAILABILITY_DOMAIN:?Set OCI_AVAILABILITY_DOMAIN, e.g. Uocm:PHX-AD-1}"
: "${OCI_IMAGE_ID:?Set OCI_IMAGE_ID to an ARM (aarch64) base image OCID}"
: "${SSH_PUBLIC_KEY_FILE:?Set SSH_PUBLIC_KEY_FILE, e.g. ~/.ssh/id_ed25519.pub}"
: "${SSH_PRIVATE_KEY_FILE:?Set SSH_PRIVATE_KEY_FILE, e.g. ~/.ssh/id_ed25519}"

INSTANCE_DISPLAY_NAME="${INSTANCE_DISPLAY_NAME:-hivemind-oci-free}"
OCPUS="${OCPUS:-2}"
MEMORY_GB="${MEMORY_GB:-12}"
SSH_USER="${SSH_USER:-ubuntu}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
K8S_DIR="${REPO_ROOT}/deploy/k8s"

echo "== [1/4] launching Always-Free ARM instance '${INSTANCE_DISPLAY_NAME}' (${OCPUS} OCPU / ${MEMORY_GB}GB) =="
echo "   (this is a REAL oci CLI call -- review before running; comment out to dry-run)"
INSTANCE_ID="$(oci compute instance launch \
  --compartment-id "${OCI_COMPARTMENT_ID}" \
  --availability-domain "${OCI_AVAILABILITY_DOMAIN}" \
  --display-name "${INSTANCE_DISPLAY_NAME}" \
  --image-id "${OCI_IMAGE_ID}" \
  --subnet-id "${OCI_SUBNET_ID}" \
  --shape "VM.Standard.A1.Flex" \
  --shape-config "{\"ocpus\": ${OCPUS}, \"memoryInGBs\": ${MEMORY_GB}}" \
  --assign-public-ip true \
  --metadata "{\"ssh_authorized_keys\": \"$(cat "${SSH_PUBLIC_KEY_FILE}")\"}" \
  --wait-for-state RUNNING \
  --query 'data.id' --raw-output)"
echo "   instance OCID: ${INSTANCE_ID}"

echo "== [2/4] fetching public IP =="
PUBLIC_IP="$(oci compute instance list-vnics \
  --instance-id "${INSTANCE_ID}" \
  --query 'data[0]."public-ip"' --raw-output)"
echo "   public IP: ${PUBLIC_IP}"

SSH_OPTS=(-i "${SSH_PRIVATE_KEY_FILE}" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)

echo "== [3/4] waiting for SSH and installing k3s =="
for _ in $(seq 1 30); do
  if ssh "${SSH_OPTS[@]}" "${SSH_USER}@${PUBLIC_IP}" true 2>/dev/null; then
    break
  fi
  sleep 10
done
# k3s's official install script (single-binary k3s server, no external etcd needed -- the
# right-sized choice for one small free-tier VM, matching the "single small OCI Always-Free
# VM(s)" target rather than a full multi-node HA control plane).
ssh "${SSH_OPTS[@]}" "${SSH_USER}@${PUBLIC_IP}" \
  'curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--write-kubeconfig-mode 644" sh -'

echo "== [4/4] copying deploy/k8s/ manifests and applying =="
scp "${SSH_OPTS[@]}" -r "${K8S_DIR}" "${SSH_USER}@${PUBLIC_IP}:/tmp/hivemind-k8s"
ssh "${SSH_OPTS[@]}" "${SSH_USER}@${PUBLIC_IP}" \
  'sudo k3s kubectl apply -f /tmp/hivemind-k8s/'

cat <<EOF

== done (if every step above succeeded) ==
NOTE: this script does NOT push deploy/{engine,api,agents,ui} images to a registry for you.
The four deploy/k8s/*-deployment.yaml manifests reference local ':kindtest'-tagged images by
default (this subtask's local kind-validation tag) -- before applying on a real k3s node you
must either:
  (a) build+push the four images to a registry the k3s node can pull from and update each
      manifest's 'image:' + set 'imagePullPolicy: IfNotPresent' or 'Always' accordingly, or
  (b) build the images directly on the VM (k3s uses containerd, so 'ctr images import' after
      a local docker save is also an option for a single-node free-tier setup with no
      registry).
See deploy/oci/README.md and deploy/k8s/README.md for details. This is intentionally left as
a manual/reviewed step, not automated here, since it is account/registry-specific.

Check status once done:
  ssh -i ${SSH_PRIVATE_KEY_FILE} ${SSH_USER}@${PUBLIC_IP} sudo k3s kubectl get pods -n hivemind
EOF
