#!/bin/bash
#
# KubeVirtCI integration functions
# Based on kubevirt/hyperconverged-cluster-operator

KUBEVIRTCI_VERSION=${KUBEVIRTCI_VERSION:-"2603261733-5efa6c07"}
KUBEVIRT_PROVIDER=${KUBEVIRT_PROVIDER:-"k8s-1.35"}
KUBEVIRTCI_PATH="${KUBEVIRTCI_PATH:-$PWD/_kubevirtci}"

function kubevirtci::install() {
    if [ ! -d "${KUBEVIRTCI_PATH}" ]; then
        echo "Installing kubevirtci ${KUBEVIRTCI_VERSION}..."
        git clone https://github.com/kubevirt/kubevirtci.git "${KUBEVIRTCI_PATH}"
        (
            cd "${KUBEVIRTCI_PATH}"
            git checkout "${KUBEVIRTCI_VERSION}"
        )
    fi
}

function kubevirtci::path() {
    echo -n "${KUBEVIRTCI_PATH}"
}

function kubevirtci::kubeconfig() {
    echo -n "${KUBEVIRTCI_PATH}/_ci-configs/${KUBEVIRT_PROVIDER}/.kubeconfig"
}
