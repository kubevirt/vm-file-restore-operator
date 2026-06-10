#!/bin/bash
#
# Deploy the operator to the kubevirtci cluster

set -e

: "${IMG:=quay.io/agilboa/vm-file-restore-operator:latest}"

echo "Deploying operator with image: ${IMG}"

# Build, push, and generate installer manifest
echo "Building image..."
make docker-build docker-push build-installer IMG="${IMG}"

# Deploy using the generated installer manifest
echo "Deploying to cluster..."
kubectl apply -f dist/install.yaml

echo ""
echo "Waiting for operator pod to be ready..."
kubectl wait --for=condition=available --timeout=60s deployment/vm-file-restore-operator -n file-restore || {
  echo ""
  echo "Warning: Deployment not ready after 60s"
  kubectl get pods -n file-restore
  echo ""
  echo "Check pod logs: kubectl logs -n file-restore -l control-plane=controller-manager"
}

echo ""
echo "Deployment status:"
kubectl get deployment -n file-restore
echo ""
kubectl get pods -n file-restore
