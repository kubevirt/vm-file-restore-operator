# HCO Integration Requirements

> **Status:** This operator is currently **standalone**. HCO integration is planned but not yet implemented. The operator can be deployed and used independently without HCO.

## Overview

To be managed by HCO (Hyperconverged Cluster Operator), the vm-file-restore-operator needs to follow the same pattern as other KubeVirt ecosystem operators (KubeVirt, CDI, SSP, AAQ).

**Current Implementation:** The operator currently implements the restore logic (VirtualMachineFileRestore CR and controller) and is fully functional as a standalone operator.

## HCO Architecture Pattern

```
User creates HyperConverged CR
  ↓
HCO reconciles and creates operator CRs:
  - KubeVirt
  - CDI
  - SSP
  - NetworkAddonsConfig
  - AAQ
  - FileRestoreOperator ← YOU
  ↓
Each operator watches its own CR
```

## What You Need

### 1. **Operator-Level CR** (Missing!)

You currently have:
- ✅ VirtualMachineFileRestore CR (the restore job)

You need:
- ❌ **FileRestoreOperator CR** (operator configuration)

**Example from SSP:**
```yaml
apiVersion: ssp.kubevirt.io/v1beta2
kind: SSP
metadata:
  name: ssp
  namespace: kubevirt-hyperconverged
spec:
  templateValidator:
    replicas: 2
  commonTemplates:
    namespace: openshift
```

**You need:**
```yaml
apiVersion: filerestore.kubevirt.io/v1beta1
kind: FileRestoreOperator
metadata:
  name: vm-file-restore-operator
  namespace: kubevirt-hyperconverged
spec:
  # Operator configuration
  replicas: 1
  infra: {}       # Node placement
  workloads: {}   # Workload config
```

### 2. **Two-Level CRD Structure**

**Level 1: Operator Configuration CR** (Created by HCO)
- Kind: `FileRestoreOperator`
- Configures the operator itself
- Set by HCO based on HyperConverged CR settings
- Controls: replicas, node placement, feature gates

**Level 2: Workload CRs** (Created by users)
- Kind: `VirtualMachineFileRestore`  ← You already have this!
- The actual restore jobs
- Created by cluster users/admins
- Watched by your operator controller

### 3. **Operator Structure Changes**

Current:
```
vm-file-restore-operator/
├── api/v1alpha1/
│   └── virtualmachinefilerestore_types.go  ← Restore job CR
└── internal/controller/
    └── virtualmachinefilerestore_controller.go
```

Need:
```
vm-file-restore-operator/
├── api/
│   ├── v1beta1/                         ← NEW
│   │   └── filerestoreoperator_types.go      # Operator config CR
│   └── v1alpha1/
│       └── virtualmachinefilerestore_types.go # Restore job CR
└── internal/controller/
    ├── filerestoreoperator_controller.go      ← NEW (HCO manages this)
    └── virtualmachinefilerestore_controller.go # Restore logic
```

### 4. **HCO Integration Points**

**In HCO repository, add:**

1. **Operand handler** (in HCO repo):
```go
// controllers/operands/filerestoreoperator.go
func newFileRestoreOperator(hc *hcov1beta1.HyperConverged) *operands.FileRestoreOperatorHandler {
    return &operands.FileRestoreOperatorHandler{
        // Creates FileRestoreOperator CR
    }
}
```

2. **Add to HyperConverged CR** (in HCO repo):
```go
// api/v1beta1/hyperconverged_types.go
type HyperConvergedSpec struct {
    // ... existing fields
    FileRestore *FileRestoreConfig `json:"fileRestore,omitempty"`
}
```

3. **Add to reconcile loop** (in HCO repo):
```go
// controllers/hyperconverged/hyperconverged_controller.go
func (r *HyperConvergedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // ... existing operands

    // Ensure FileRestoreOperator CR exists
    if err := r.ensureFileRestoreOperator(hc); err != nil {
        return ctrl.Result{}, err
    }
}
```

## Implementation Steps

### Phase 1: Add Operator-Level CR

1. Create new API version:
```bash
kubebuilder create api --group restore --version v1beta1 --kind FileRestoreOperator
```

2. Define FileRestoreOperator spec:
```go
type FileRestoreOperatorSpec struct {
    // Infra configures resources for operator pod
    Infra HyperConvergedConfig `json:"infra,omitempty"`

    // Workloads configures resources for restore workloads
    Workloads HyperConvergedConfig `json:"workloads,omitempty"`

    // FeatureGates enables optional features
    FeatureGates FileRestoreFeatureGates `json:"featureGates,omitempty"`
}
```

3. Create controller that:
   - Watches FileRestoreOperator CR
   - Creates/manages operator Deployment
   - Sets defaults from HCO

### Phase 2: Update Existing Controller

Your VirtualMachineFileRestore controller:
- Stays the same
- Continues watching VirtualMachineFileRestore CRs
- Reads configuration from FileRestoreOperator CR (if needed)

### Phase 3: HCO Integration

Submit PR to HCO repository adding:
1. Operand handler for FileRestoreOperator
2. Field in HyperConverged CR
3. RBAC for managing FileRestoreOperator CRs
4. Default configuration

## Example from Real HCO-Managed Operators

### SSP Operator Structure

```
ssp-operator/
├── api/v1beta2/
│   └── ssp_types.go           # SSP CR (operator config)
└── internal/operands/
    ├── template-validator/     # Workload 1
    ├── vm-console-proxy/       # Workload 2
    └── common-templates/       # Workload 3
```

**SSP CR is created by HCO**
**SSP operator creates the actual workloads**

### Your Pattern Should Be

```
vm-file-restore-operator/
├── api/
│   ├── v1beta1/
│   │   └── filerestoreoperator_types.go  # Created by HCO
│   └── v1alpha1/
│       └── virtualmachinefilerestore_types.go  # Created by users
└── controllers/
    ├── filerestoreoperator_controller.go     # Manages operator deployment
    └── virtualmachinefilerestore_controller.go  # Manages restores
```

## Key Differences from Standalone

| Aspect | Standalone | HCO-Managed |
|--------|------------|-------------|
| **Deployment** | User runs `kubectl apply -f install.yaml` | HCO creates operator CR |
| **Configuration** | Direct operator Deployment | FileRestoreOperator CR |
| **RBAC** | User creates manually | HCO manages |
| **Lifecycle** | Independent | HCO upgrades/manages |
| **Integration** | None | Part of HyperConverged stack |

## Benefits of HCO Integration

1. **Unified Management**: Deploy entire stack with one CR
2. **Coordinated Upgrades**: HCO manages version compatibility
3. **Consistent Configuration**: Infra/workload placement across all operators
4. **Feature Gates**: Cluster-wide feature management
5. **OLM Integration**: Operator Lifecycle Manager support
6. **Production Ready**: Matches other KubeVirt ecosystem operators

## Next Steps

1. **Add operator-level CR** (FileRestoreOperator)
2. **Test standalone** (still works without HCO)
3. **Contact HCO maintainers** about integration
4. **Submit PR to HCO** with operand handler
5. **Coordinate release** with HCO team

## References

- HCO Repository: https://github.com/kubevirt/hyperconverged-cluster-operator
- SSP Operator: https://github.com/kubevirt/ssp-operator
- CDI: https://github.com/kubevirt/containerized-data-importer
- AAQ: https://github.com/kubevirt/application-aware-quota
