#!/usr/bin/env bash
#
# Ensure kubevirtci is available for cluster management.
# Downloads on first use, reuses on subsequent calls.
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
KUBEVIRTCI_DIR="${REPO_ROOT}/kubevirtci"

if [ ! -d "${KUBEVIRTCI_DIR}" ]; then
    echo "Downloading kubevirtci (first use only)..."
    # Clone unpinned for wrapper scripts; actual provider images are controlled by KUBEVIRTCI_TAG
    git clone --depth 1 https://github.com/kubevirt/kubevirtci.git "${KUBEVIRTCI_DIR}"
    echo "kubevirtci downloaded to ${KUBEVIRTCI_DIR}"
fi

echo "Using kubevirtci from ${KUBEVIRTCI_DIR}"
