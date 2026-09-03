#!/usr/bin/env bash
#
# Resolve operator image names from kubevirtci after cluster-up.
# Host pushes to docker_prefix (localhost:<port>/kubevirt); the cluster pulls
# manifest_docker_prefix (registry:5000/kubevirt).

set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=config.sh
source "${SCRIPT_DIR}/config.sh"

export KUBEVIRTCI_PATH="${KUBEVIRTCI_PATH:-${REPO_ROOT}/kubevirtci/cluster-up/}"
export KUBEVIRTCI_CONFIG_PATH="${KUBEVIRTCI_CONFIG_PATH:-${REPO_ROOT}/kubevirtci/_ci-configs}"

provider_config="${KUBEVIRTCI_CONFIG_PATH}/${KUBEVIRT_PROVIDER}/config-provider-${KUBEVIRT_PROVIDER}.sh"
if [ ! -f "${provider_config}" ]; then
	if [ -n "${IMG:-}" ] && [ -n "${PUSH_IMG:-}" ]; then
		return 0 2>/dev/null || exit 0
	fi
	echo "Error: ${provider_config} not found. Run 'make cluster-up' before cluster-sync." >&2
	exit 1
fi

# shellcheck source=../kubevirtci/cluster-up/hack/config.sh
source "${KUBEVIRTCI_PATH%/}/hack/config.sh"

image_name="vm-file-restore-operator"
image_tag="${IMAGE_TAG:-dev-$(git -C "${REPO_ROOT}" rev-parse --short HEAD)}"

if [ -z "${PUSH_IMG:-}" ]; then
	export PUSH_IMG="${docker_prefix}/${image_name}:${image_tag}"
fi
if [ -z "${IMG:-}" ]; then
	export IMG="${manifest_docker_prefix}/${image_name}:${image_tag}"
fi

echo "Using PUSH_IMG=${PUSH_IMG}" >&2
echo "Using IMG=${IMG}" >&2
