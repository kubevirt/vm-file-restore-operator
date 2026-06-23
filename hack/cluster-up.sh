#!/usr/bin/env bash
#
# Bring up a kubevirtci development cluster
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Ensure kubevirtci is available
"${SCRIPT_DIR}/ensure-kubevirtci.sh"

# Source kubevirtci cluster-up
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
export KUBEVIRTCI_PATH="${REPO_ROOT}/kubevirtci/cluster-up/"

# Set provider (k8s version) - use latest stable
export KUBEVIRT_PROVIDER="${KUBEVIRT_PROVIDER:-k8s-1.36}"

# Set number of nodes (1 master + N workers)
export KUBEVIRT_NUM_NODES="${KUBEVIRT_NUM_NODES:-2}"

# Always deploy CDI (required for DataVolumes and snapshots)
export KUBEVIRT_DEPLOY_CDI=true

# KubeVirt version to deploy (can be overridden via KUBEVIRT_VERSION env var)
# Pinned to a known-good version for reproducibility
KUBEVIRT_VERSION="${KUBEVIRT_VERSION:-v1.8.4}"
echo "Using KubeVirt version: ${KUBEVIRT_VERSION}"

# Set kubevirtci tag (determines gocli version to use)
# Pinned to a known-good version for reproducibility (can be overridden via KUBEVIRTCI_TAG env var)
export KUBEVIRTCI_TAG="${KUBEVIRTCI_TAG:-2606221522-c3d11ec0}"
echo "Using kubevirtci tag: ${KUBEVIRTCI_TAG}"

# Bring up the cluster
source "${KUBEVIRTCI_PATH}up.sh"

# Deploy KubeVirt
echo "Deploying KubeVirt ${KUBEVIRT_VERSION}..."
kubectl apply -f "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-operator.yaml"
kubectl apply -f "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-cr.yaml"

# Enable DeclarativeHotplugVolumes feature gate (required for VM file restore)
echo "Enabling DeclarativeHotplugVolumes feature gate..."
kubectl patch kubevirt kubevirt -n kubevirt --type=merge -p '{"spec":{"configuration":{"developerConfiguration":{"featureGates":["DeclarativeHotplugVolumes"]}}}}'

# Wait for KubeVirt to be ready
echo "Waiting for KubeVirt to be ready..."
kubectl wait --for=condition=Available --timeout=5m -n kubevirt kv kubevirt
echo "KubeVirt ${KUBEVIRT_VERSION} is ready"

# Wait for CDI to be ready (deployed by kubevirtci via KUBEVIRT_DEPLOY_CDI=true)
if [ "${KUBEVIRT_DEPLOY_CDI}" == "true" ]; then
	echo "Waiting for CDI to be ready..."
	kubectl wait --for=condition=Available --timeout=5m -n cdi cdi cdi
	CDI_VERSION=$(kubectl get cdi cdi -n cdi -o jsonpath='{.status.observedVersion}')
	echo "CDI ${CDI_VERSION} is ready"
fi

echo "Cluster ready with KubeVirt ${KUBEVIRT_VERSION}$([ "${KUBEVIRT_DEPLOY_CDI}" == "true" ] && echo " and CDI ${CDI_VERSION}")"
