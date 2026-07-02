#!/usr/bin/env bash
#
# Shared configuration for kubevirtci cluster scripts.
# Sourced by cluster management scripts (hack/cluster-*.sh, Makefile).
# Update defaults here to avoid drift across scripts.
#

SCRIPT_DIR_CONFIG="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR_CONFIG}/.." && pwd)"
export REPO_ROOT

# Kubernetes provider version for kubevirtci
export KUBEVIRT_PROVIDER="${KUBEVIRT_PROVIDER:-k8s-1.36}"

# kubevirtci tag — determines which gocli version manages cluster containers
# Pinned to a known-good version for reproducibility
export KUBEVIRTCI_TAG="${KUBEVIRTCI_TAG:-2606221522-c3d11ec0}"

# Path to kubevirtci cluster-up scripts
export KUBEVIRTCI_PATH="${REPO_ROOT}/kubevirtci/cluster-up/"
