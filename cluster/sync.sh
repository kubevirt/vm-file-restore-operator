#!/bin/bash
#
# Deploy the operator to the kubevirtci cluster

set -e

if [ -z "${KUBECONFIG}" ]; then
    echo "Error: KUBECONFIG is not set. Run 'eval \"\$(make cluster-kubeconfig)\"' first." >&2
    exit 1
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ -z "${IMG:-}" ] && [ -z "${PUSH_IMG:-}" ]; then
    # shellcheck source=../hack/kubevirtci-image-env.sh
    source "${repo_root}/hack/kubevirtci-image-env.sh"
elif [ -z "${PUSH_IMG:-}" ]; then
    export PUSH_IMG="${IMG}"
elif [ -z "${IMG:-}" ]; then
    export IMG="${PUSH_IMG}"
fi

echo "Deploying operator with image: ${IMG}"
if [ "${PUSH_IMG}" != "${IMG}" ]; then
    echo "Pushing image as: ${PUSH_IMG}"
fi

if [ "${SKIP_IMAGE_BUILD:-}" = "true" ]; then
    echo "Skipping image build (SKIP_IMAGE_BUILD=true)"
    make build-installer IMG="${IMG}"
else
    echo "Building and pushing operator image..."
    make docker-build docker-push IMG="${PUSH_IMG}"
    make build-installer IMG="${IMG}"
fi

echo "Deploying to cluster..."
kubectl apply -f dist/install.yaml

# Restart deployment so the new image reference is picked up. With a unique tag per
# commit, the cluster pulls the freshly pushed image even with imagePullPolicy: IfNotPresent.
echo ""
echo "Restarting deployment to pull new image..."
kubectl rollout restart deployment/vm-file-restore-operator -n file-restore

echo ""
echo "Waiting for rollout to complete..."
kubectl rollout status deployment/vm-file-restore-operator -n file-restore --timeout=60s || {
  echo ""
  echo "Warning: Rollout not complete after 60s"
  kubectl get pods -n file-restore
  echo ""
  echo "Check pod logs: kubectl logs -n file-restore -l control-plane=controller-manager"
}

echo ""
echo "Deployment status:"
kubectl get deployment -n file-restore
echo ""
kubectl get pods -n file-restore

# Reset kustomization.yaml to avoid polluting git status
echo ""
echo "Cleaning up local kustomization changes..."
git restore config/manager/kustomization.yaml 2>/dev/null || true
