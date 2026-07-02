#!/usr/bin/env bash
#
# Tear down the kubevirtci development cluster
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Load shared kubevirtci configuration
source "${SCRIPT_DIR}/config.sh"

KUBEVIRTCI_DIR="${REPO_ROOT}/kubevirtci"

if [ ! -d "${KUBEVIRTCI_DIR}" ]; then
    echo "No kubevirtci cluster found"
    exit 0
fi

echo "Using kubevirtci tag: ${KUBEVIRTCI_TAG}"

# Tear down the cluster
"${KUBEVIRTCI_DIR}/cluster-up/down.sh"
