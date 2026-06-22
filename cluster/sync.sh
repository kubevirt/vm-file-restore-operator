#!/bin/bash
#
# Deploy the operator to the kubevirtci cluster

set -e

: "${IMG:=quay.io/kubevirt/vm-file-restore-operator:latest}"

echo "Deploying operator with image: ${IMG}"

# Build, push, and generate installer manifest
echo "Building image..."
make docker-build docker-push build-installer IMG="${IMG}"

# Deploy using the generated installer manifest
echo "Deploying to cluster..."
kubectl apply -f dist/install.yaml

# Restart deployment to pick up new image (imagePullPolicy: IfNotPresent requires pod recreation)
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
