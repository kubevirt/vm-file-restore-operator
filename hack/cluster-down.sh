#!/usr/bin/env bash
#
# Tear down the kubevirtci development cluster
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
KUBEVIRTCI_DIR="${REPO_ROOT}/kubevirtci"

if [ ! -d "${KUBEVIRTCI_DIR}" ]; then
    echo "No kubevirtci cluster found"
    exit 0
fi

# Set kubevirtci tag (determines gocli version to use)
# Pinned to a known-good version for reproducibility (can be overridden via KUBEVIRTCI_TAG env var)
export KUBEVIRTCI_TAG="${KUBEVIRTCI_TAG:-2606221522-c3d11ec0}"
echo "Using kubevirtci tag: ${KUBEVIRTCI_TAG}"

# Tear down the cluster
"${KUBEVIRTCI_DIR}/cluster-up/down.sh"
