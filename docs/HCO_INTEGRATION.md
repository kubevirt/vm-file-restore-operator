# HCO Integration

> **Status:** HCO integration support **implemented**. The operator can be deployed standalone or managed by HCO (HyperConverged Cluster Operator).

## Overview

The vm-file-restore-operator follows the standard HCO integration pattern used by other KubeVirt ecosystem operators (KubeVirt, CDI, SSP, AAQ). It can operate in two modes:

1. **Standalone**: Deploy directly using `kubectl apply` or `make deploy`
2. **HCO-managed**: HCO creates and manages the FileRestoreOperator CR

## HCO Architecture

```
User creates HyperConverged CR
  ↓
HCO reconciles and creates operator CRs:
  - KubeVirt
  - CDI
  - SSP
  - NetworkAddonsConfig
  - AAQ
  - FileRestoreOperator ✅
  ↓
FileRestoreOperator controller reconciles configuration
  ↓
Users create VirtualMachineFileRestore CRs for restore operations
```

## Custom Resources

### FileRestoreOperator CR (Operator Configuration)

API group: `filerestore.kubevirt.io/v1alpha1`

The FileRestoreOperator CR configures the operator itself and is managed by HCO:

```yaml
apiVersion: filerestore.kubevirt.io/v1alpha1
kind: FileRestoreOperator
metadata:
  name: vm-file-restore-operator
  namespace: file-restore
spec:
  imagePullPolicy: IfNotPresent
  infra: {}
  workloads: {}
  tlsSecurityProfile:
    type: Intermediate
```

**Spec Fields:**
- `imagePullPolicy`: Pull policy for operator images
- `infra`: Node placement for infrastructure pods (future)
- `workloads`: Node placement for workload pods (future)
- `tlsSecurityProfile`: TLS configuration (Old, Intermediate, Modern, Custom)

**Status Fields:**
- `phase`: Deployment phase (Deploying, Deployed, Error)
- `operatorVersion`: Current operator version
- `observedGeneration`: Last reconciled generation

### VirtualMachineFileRestore CR (Restore Operations)

API group: `filerestore.kubevirt.io/v1alpha1`

Users create VirtualMachineFileRestore CRs to perform file restore operations (unchanged from standalone mode):

```yaml
apiVersion: filerestore.kubevirt.io/v1alpha1
kind: VirtualMachineFileRestore
metadata:
  name: restore-my-files
spec:
  virtualMachineName: my-vm
  source:
    volumeSnapshot:
      name: vm-snapshot-1
  targetPaths:
    - /home/user/document.txt
```

## TLS Security Profiles

The operator supports OpenShift-style TLS security profiles for metrics server:

- **Old**: TLS 1.0+, maximum compatibility
- **Intermediate** (default): TLS 1.2+, balanced security/compatibility
- **Modern**: TLS 1.3+, maximum security
- **Custom**: User-defined ciphersuites and min TLS version

Example custom profile:

```yaml
spec:
  tlsSecurityProfile:
    type: Custom
    custom:
      ciphers:
        - ECDHE-RSA-AES128-GCM-SHA256
        - ECDHE-RSA-AES256-GCM-SHA384
      minTLSVersion: VersionTLS13
```

## OLM Integration

The operator includes a CSV (ClusterServiceVersion) generator for OLM bundle creation:

```bash
# Generate CSV bundle
make bundle VERSION=1.0.0 PREV_VERSION=0.9.0

# The csv-generator binary is built into the operator image at:
/usr/bin/csv-generator

# HCO invokes it to generate CSV manifests dynamically
```

## Deployment

### Standalone Deployment

```bash
# Install CRDs
make install

# Deploy operator
make deploy IMG=quay.io/kubevirt/vm-file-restore-operator:latest

# Create FileRestoreOperator CR (optional, for TLS config)
kubectl apply -f config/samples/restore_v1alpha1_filerestoreoperator.yaml
```

### HCO-Managed Deployment

HCO automatically creates and manages the FileRestoreOperator CR. Users only interact with VirtualMachineFileRestore CRs for restore operations.

## Implementation Details

### Controller

The FileRestoreOperator controller:
- Watches FileRestoreOperator CRs
- Updates status to `Deployed` when reconciled
- Tracks `observedGeneration` for spec changes
- Provides TLS configuration to the metrics server

### ManagedTLSWatcher

Dynamically updates TLS configuration based on the FileRestoreOperator CR's `tlsSecurityProfile`:
- Monitors FileRestoreOperator resources
- Applies TLS changes to metrics server without restart
- Falls back to Intermediate profile if no CR exists

### CSV Generator

Binary tool that generates ClusterServiceVersion manifests for OLM:
- Embeds CRD definitions
- Generates RBAC rules
- Outputs deployment specifications
- Supports upgrade paths via `--replaces-csv-version`

## Backward Compatibility

The operator maintains full backward compatibility:
- Existing VirtualMachineFileRestore workflows unchanged
- FileRestoreOperator CR is optional for standalone deployments
- TLS defaults to Intermediate profile if not configured

## See Also

- [Design Specification](superpowers/specs/2026-06-21-hco-integration-design.md)
- [HCO Requirements Analysis](HCO_REQUIREMENTS_ANALYSIS.md)
- [Certificate Rotation](CERTIFICATE_ROTATION.md) (planned for webhook support)
