# HCO Integration Design for vm-file-restore-operator

**Date:** 2026-06-21  
**Status:** Approved

## Overview

Transform vm-file-restore-operator from standalone to HCO-ready following HCO integration requirements. This enables the operator to be managed by HyperConverged Cluster Operator (HCO) while maintaining standalone deployment capability.

## Background

### HCO Requirements

The following are required for HCO integration:

1. **operator-sdk** - for API scaffolding and OLM integration
2. **CSV generator** - binary in operator image that writes ClusterServiceVersion and CRDs to stdout
3. **Certificate rotation** - automated cert renewal (deferred until webhooks are added)
4. **TLS configuration** - minTLSVersion + cipherSuites support

### HCO Integration Pattern

HCO-managed operators follow this pattern:
- Single operator-level CR for configuration in v1alpha1
- Uses controller-lifecycle-operator-sdk for lifecycle management
- CSV generator binary at `tools/csv-generator/`
- TLS configuration via ManagedTLSWatcher
- cert-manager for certificate management (when webhooks present)

## Current vs Proposed Architecture

### Current Architecture

```
User creates VirtualMachineFileRestore CR
  ↓
Operator reconciles and performs restore
```

**Limitations:**
- No configuration surface for HCO
- No OLM metadata generation
- No standardized status reporting
- No TLS configuration support

### Proposed Architecture

```
HCO creates FileRestoreOperator CR (operator config)
  ↓
FileRestoreOperator controller reconciles (manages operator deployment/config)
  ↓
User creates VirtualMachineFileRestore CR (restore job)
  ↓
VirtualMachineFileRestore controller reconciles (performs restore)
```

**Two-level CR structure:**
- **Level 1:** `FileRestoreOperator` - operator configuration (created by HCO or user)
- **Level 2:** `VirtualMachineFileRestore` - restore operations (created by users)

**Still works standalone:**
- Users can deploy operator + default FileRestoreOperator CR
- Existing workflows (`make cluster-sync`, `kubectl apply -f install.yaml`) continue working
- No breaking changes to VirtualMachineFileRestore CRs

## Design Details

### 1. New API: FileRestoreOperator CR

**Location:** `api/v1alpha1/filerestoreoperator_types.go`

**Type Definition:**
```go
package v1alpha1

import (
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    sdkapi "kubevirt.io/controller-lifecycle-operator-sdk/api"
)

// FileRestoreOperatorSpec defines the desired state of FileRestoreOperator
type FileRestoreOperatorSpec struct {
    // ImagePullPolicy describes a policy for if/when to pull container images
    // +kubebuilder:validation:Enum=Always;IfNotPresent;Never
    ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
    
    // Infra configures node placement for operator pod
    // +optional
    Infra sdkapi.NodePlacement `json:"infra,omitempty"`
    
    // Workloads configures resources for restore operations
    // +optional
    Workloads sdkapi.NodePlacement `json:"workloads,omitempty"`
    
    // TLSSecurityProfile configures TLS settings for metrics server
    // +optional
    TLSSecurityProfile *TLSSecurityProfile `json:"tlsSecurityProfile,omitempty"`
}

// FileRestoreOperatorStatus defines the observed state of FileRestoreOperator
type FileRestoreOperatorStatus struct {
    sdkapi.Status `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type FileRestoreOperator struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   FileRestoreOperatorSpec   `json:"spec,omitempty"`
    Status FileRestoreOperatorStatus `json:"status,omitempty"`
}
```

**Purpose:**
- Configuration surface for HCO to manage
- Node placement control (operator vs workload pods)
- TLS security settings
- Status reporting using standardized sdk patterns

**Status includes (from sdkapi.Status):**
- `Phase` - Deployed, Deploying, Deleting, Deleted
- `Conditions` - Available, Progressing, Degraded
- `ObservedGeneration` - spec change tracking
- `OperatorVersion` - deployed version
- `TargetVersion` - desired version

### 2. TLS Types

**Location:** `api/v1alpha1/types_tlssecurityprofile.go`

**TLS Security Profile Types:**

This file contains:
- `TLSSecurityProfile` struct with union type (Old, Intermediate, Modern, Custom)
- Profile type structs: `OldTLSProfile`, `IntermediateTLSProfile`, `ModernTLSProfile`, `CustomTLSProfile`
- `TLSProfileSpec` with `Ciphers []string` and `MinTLSVersion TLSProtocolVersion`
- `TLSProtocolVersion` type (e.g., `VersionTLS10`, `VersionTLS12`, `VersionTLS13`)
- `TLSProfiles` map with predefined profiles:
  ```go
  var TLSProfiles = map[TLSProfileType]*TLSProfileSpec{
      TLSProfileOldType: {
          Ciphers: []string{...},
          MinTLSVersion: VersionTLS10,
      },
      TLSProfileIntermediateType: {
          Ciphers: []string{...},
          MinTLSVersion: VersionTLS12,
      },
      // ... Modern profile
  }
  ```

**Why copy instead of importing:**
- Avoids OpenShift API dependency
- Matches existing HCO-managed operators pattern
- Full control over TLS profile definitions
- Simpler dependency tree

### 3. FileRestoreOperator Controller

**Location:** `internal/controller/filerestoreoperator_controller.go`

**Responsibilities (Phase 1):**
- Watch FileRestoreOperator CR
- Update status:
  - Set `Phase: Deployed`
  - Set `Conditions: Available=True`
  - Update `ObservedGeneration`
  - Record `OperatorVersion`
- Use controller-lifecycle-operator-sdk reconciler patterns

**Reconcile Logic:**
```go
func (r *FileRestoreOperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // Fetch FileRestoreOperator CR
    // Update status using sdkr.Reconciler helpers
    // Set Phase: Deployed
    // Set Available condition: True
    // Record version
    return ctrl.Result{}, nil
}
```

**Future enhancements (Phase 2+):**
- Read spec.Infra and apply to operator deployment
- Read spec.Workloads and configure restore pods
- Read spec.ImagePullPolicy and apply
- Manage operator deployment updates

**VirtualMachineFileRestore Controller:**
- ✅ **No changes** - stays exactly as-is
- ✅ Continues watching VirtualMachineFileRestore CRs
- ✅ All existing restore logic unchanged
- Could optionally read defaults from FileRestoreOperator CR in future

### 4. CSV Generator

**Location:** `tools/csv-generator/csv-generator.go`

**Purpose:**
- Generate ClusterServiceVersion for OLM
- Output CRDs to stdout
- Provide metadata for HCO integration

**Command-line Interface:**
```go
var (
    csvVersion         = flag.String("csv-version", "", "")
    replacesCsvVersion = flag.String("replaces-csv-version", "", "")
    namespace          = flag.String("namespace", "", "")
    pullPolicy         = flag.String("pull-policy", "", "")
    operatorImage      = flag.String("operator-image", "", "")
    operatorVersion    = flag.String("operator-version", "", "")
    dumpCRDs           = flag.Bool("dump-crds", false, "dump CRD manifests to stdout")
)
```

**Usage by HCO:**
```bash
csv-generator \
  --csv-version=1.0.0 \
  --replaces-csv-version=0.9.0 \
  --namespace=kubevirt-hyperconverged \
  --operator-image=quay.io/kubevirt/vm-file-restore-operator:v1.0.0 \
  --operator-version=1.0.0 \
  --dump-crds

# Outputs to stdout:
# 1. ClusterServiceVersion YAML
# 2. FileRestoreOperator CRD YAML
# 3. VirtualMachineFileRestore CRD YAML
```

**Implementation:**
```go
package main

import (
    _ "embed"
    "flag"
    "os"
    
    operator "kubevirt.io/vm-file-restore-operator/pkg/resources/operator"
)

//go:embed assets/restore.kubevirt.io_filerestoreoperators.yaml
var fileRestoreOperatorCRD []byte

//go:embed assets/restore.kubevirt.io_virtualmachinefilerestores.yaml
var vmFileRestoreCRD []byte

func main() {
    flag.Parse()
    
    data := operator.ClusterServiceVersionData{
        CsvVersion:         *csvVersion,
        ReplacesCsvVersion: *replacesCsvVersion,
        Namespace:          *namespace,
        ImagePullPolicy:    *pullPolicy,
        OperatorVersion:    *operatorVersion,
        OperatorImage:      *operatorImage,
        // RBAC rules, deployment spec, etc.
    }
    
    csv, err := operator.NewClusterServiceVersion(&data)
    if err != nil {
        panic(err)
    }
    
    // Output CSV to stdout
    if err = marshallObject(csv, os.Stdout); err != nil {
        panic(err)
    }
    
    // Output CRDs if requested
    if *dumpCRDs {
        os.Stdout.Write(fileRestoreOperatorCRD)
        os.Stdout.Write(vmFileRestoreCRD)
    }
}
```

**Helper Package:**

**Location:** `pkg/resources/operator/csv.go`

```go
package operator

import (
    csvv1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
)

type ClusterServiceVersionData struct {
    CsvVersion         string
    ReplacesCsvVersion string
    Namespace          string
    ImagePullPolicy    string
    OperatorVersion    string
    OperatorImage      string
}

func NewClusterServiceVersion(data *ClusterServiceVersionData) (*csvv1.ClusterServiceVersion, error) {
    // Build CSV with:
    // - Metadata (name, namespace, labels, annotations)
    // - Spec:
    //   - displayName, description, version
    //   - replaces (previous version)
    //   - install (deployment spec, RBAC)
    //   - customresourcedefinitions (owned CRDs)
    //   - maintainers, provider, links
    return csv, nil
}
```

### 5. TLS Configuration

**Location:** `cmd/main.go` updates

**ManagedTLSWatcher Implementation:**
```go
import (
    "crypto/tls"
    "context"
    
    ocpconfigv1 "github.com/openshift/api/config/v1"
    ctrl "sigs.k8s.io/controller-runtime"
    
    "kubevirt.io/vm-file-restore-operator/pkg/resources/utils"
)

func main() {
    // ... existing setup ...
    
    // Create TLS watcher
    managedTLSWatcher := utils.NewManagedTLSWatcher()
    
    // Configure dynamic TLS
    cryptoPolicyOpt := func(c *tls.Config) {
        c.GetConfigForClient = func(t *tls.ClientHelloInfo) (*tls.Config, error) {
            config := c.Clone()
            if managedTLSWatcher != nil {
                ctx := t.Context()
                cc := managedTLSWatcher.GetTLSConfig(ctx)
                config.CipherSuites = cc.CipherSuites
                config.MinVersion = cc.MinVersion
            }
            return config, nil
        }
    }
    
    // Apply to metrics server
    metricsServerOptions := metricsserver.Options{
        BindAddress: metricsAddr,
        SecureServing: secureMetrics,
        TLSOpts: []func(*tls.Config){cryptoPolicyOpt},
    }
    
    // ... rest of manager setup ...
}
```

**ManagedTLSWatcher Helper:**

**Location:** `pkg/resources/utils/tls_watcher.go`

**Implementation:**

Key characteristics:
- Implements `manager.Runnable` (has `Start()` and `NeedLeaderElection()` methods)
- Uses `cache.Cache` instead of direct client for efficiency
- Returns `cryptoConfig` struct with `CipherSuites []uint16` and `MinVersion uint16`
- Waits for cache sync before becoming ready
- Falls back to Intermediate profile if CR not found
- Thread-safe with `sync.RWMutex`
- Lists `FileRestoreOperatorList` from cache (not direct Get)

```go
package utils

import (
    "context"
    "crypto/tls"
    "fmt"
    "sync"
    
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/cache"
    
    "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

type cryptoConfig struct {
    CipherSuites []uint16
    MinVersion   uint16
}

type ManagedTLSWatcher struct {
    mu            sync.RWMutex
    cache         cache.Cache
    defaultConfig *cryptoConfig
    ready         bool
}

func NewManagedTLSWatcher() *ManagedTLSWatcher {
    return &ManagedTLSWatcher{
        defaultConfig: cryptoConfigFromSpec(nil), // Default: Intermediate
    }
}

func (m *ManagedTLSWatcher) SetCache(c cache.Cache) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.cache = c
}

// Start implements manager.Runnable - add as runnable in main.go
func (m *ManagedTLSWatcher) Start(ctx context.Context) error {
    if !m.cache.WaitForCacheSync(ctx) {
        return fmt.Errorf("failed to wait for caches to sync")
    }
    m.mu.Lock()
    m.ready = true
    m.mu.Unlock()
    <-ctx.Done()
    return nil
}

// NeedLeaderElection implements manager.Runnable
func (m *ManagedTLSWatcher) NeedLeaderElection() bool {
    return false
}

func (m *ManagedTLSWatcher) GetTLSConfig(ctx context.Context) *cryptoConfig {
    m.mu.RLock()
    ready := m.ready
    c := m.cache
    m.mu.RUnlock()
    
    if !ready || c == nil {
        return m.defaultConfig
    }
    
    list := &v1alpha1.FileRestoreOperatorList{}
    if err := c.List(ctx, list); err != nil || len(list.Items) == 0 {
        return m.defaultConfig
    }
    
    return cryptoConfigFromSpec(list.Items[0].Spec.TLSSecurityProfile)
}

func cryptoConfigFromSpec(profile *v1alpha1.TLSSecurityProfile) *cryptoConfig {
    cipherNames, minTLSVersion := selectCipherSuitesAndMinTLSVersion(profile)
    return &cryptoConfig{
        CipherSuites: cipherSuitesIDs(cipherNames),
        MinVersion:   tlsVersionToUint16(minTLSVersion),
    }
}

func selectCipherSuitesAndMinTLSVersion(profile *v1alpha1.TLSSecurityProfile) ([]string, v1alpha1.TLSProtocolVersion) {
    if profile == nil {
        profile = &v1alpha1.TLSSecurityProfile{
            Type: v1alpha1.TLSProfileIntermediateType,
            Intermediate: &v1alpha1.IntermediateTLSProfile{},
        }
    }
    if profile.Custom != nil {
        return profile.Custom.TLSProfileSpec.Ciphers, profile.Custom.TLSProfileSpec.MinTLSVersion
    }
    return v1alpha1.TLSProfiles[profile.Type].Ciphers, v1alpha1.TLSProfiles[profile.Type].MinTLSVersion
}

// Implementation includes:
// - tlsVersionMap
// - cipherNameToID map
// - tlsVersionToUint16() helper
// - cipherSuitesIDs() helper
```

**TLS Flow:**
```
FileRestoreOperator CR specifies tlsSecurityProfile
  ↓
ManagedTLSWatcher.GetTLSConfig() reads CR
  ↓
Parses profile (Modern, Intermediate, Old, Custom)
  ↓
Returns tls.Config with MinVersion and CipherSuites
  ↓
GetConfigForClient callback applies config per connection
  ↓
Metrics server uses configured TLS
```

### 6. Dependencies

**Add to go.mod:**
```go
require (
    // controller-lifecycle-operator-sdk for common patterns
    kubevirt.io/controller-lifecycle-operator-sdk v0.2.7
    
    // Operator Framework API for CSV generation
    github.com/operator-framework/api v0.17.6
)
```

**What each provides:**

**controller-lifecycle-operator-sdk:**
- `sdkapi.NodePlacement` - node selector, tolerations, affinity
- `sdkapi.Status` - phase, conditions, observedGeneration, versions
- `sdkr.Reconciler` - reconciliation helpers
- Standardized patterns across kubevirt operators

**Operator Framework API:**
- `v1alpha1.ClusterServiceVersion` - OLM metadata type
- CSV builder utilities

**TLS Types (no dependency needed):**
- Add `types_tlssecurityprofile.go` with standard TLS profile types
- Includes all TLS types (`TLSSecurityProfile`, `TLSProfileType`, etc.)
- Includes `TLSProfiles` map with predefined profiles (Old, Intermediate, Modern, Custom)
- No OpenShift API dependency - types are copied locally

## File Changes

### New Files

**API:**
- `api/v1alpha1/filerestoreoperator_types.go` - FileRestoreOperator CR
- `api/v1alpha1/types_tlssecurityprofile.go` - TLS security profile types
- `api/v1alpha1/zz_generated.deepcopy.go` - updated by controller-gen

**Controller:**
- `internal/controller/filerestoreoperator_controller.go` - reconciler
- `internal/controller/filerestoreoperator_controller_test.go` - tests

**CSV Generator:**
- `tools/csv-generator/csv-generator.go` - main binary
- `tools/csv-generator/assets/restore.kubevirt.io_filerestoreoperators.yaml` - embedded CRD
- `tools/csv-generator/assets/restore.kubevirt.io_virtualmachinefilerestores.yaml` - embedded CRD
- `pkg/resources/operator/csv.go` - CSV generation logic

**TLS Helpers:**
- `pkg/resources/utils/tls_watcher.go` - ManagedTLSWatcher implementation

**Config:**
- `config/samples/restore_v1alpha1_filerestoreoperator.yaml` - default CR
- `config/crd/bases/restore.kubevirt.io_filerestoreoperators.yaml` - generated CRD
- `config/rbac/filerestoreoperator_editor_role.yaml` - generated RBAC
- `config/rbac/filerestoreoperator_viewer_role.yaml` - generated RBAC

### Modified Files

**Dockerfile:**
```dockerfile
# Build csv-generator binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-${GOARCH}} \
    go build -a -o csv-generator ./tools/csv-generator/

# Copy to image
COPY --from=builder /workspace/csv-generator /usr/bin/csv-generator
```

**cmd/main.go:**
- Import controller-lifecycle-operator-sdk
- Add FileRestoreOperator controller setup
- Add ManagedTLSWatcher initialization (`managedTLSWatcher := utils.NewManagedTLSWatcher()`)
- Set cache on ManagedTLSWatcher (`managedTLSWatcher.SetCache(mgr.GetCache())`)
- Add ManagedTLSWatcher as runnable (`mgr.Add(managedTLSWatcher)`)
- Add TLS configuration callback to metrics server (using `GetConfigForClient`)

**Makefile:**
```makefile
# Build csv-generator locally for testing
.PHONY: csv-generator
csv-generator:
	go build -o bin/csv-generator ./tools/csv-generator/

# Generate bundle using csv-generator
.PHONY: bundle
bundle: manifests kustomize csv-generator
	$(KUSTOMIZE) build config/crd > tools/csv-generator/assets/crds.yaml
	./bin/csv-generator \
		--csv-version=$(VERSION) \
		--namespace=$(NAMESPACE) \
		--operator-image=$(IMG) \
		--dump-crds > bundle/manifests/vm-file-restore-operator.clusterserviceversion.yaml
```

**config/default/kustomization.yaml:**
```yaml
resources:
- ../crd
- ../rbac
- ../manager
- ../samples/restore_v1alpha1_filerestoreoperator.yaml  # NEW
```

**config/rbac/role.yaml:**
```yaml
# Add FileRestoreOperator permissions
- apiGroups:
  - restore.kubevirt.io
  resources:
  - filerestoreoperators
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - restore.kubevirt.io
  resources:
  - filerestoreoperators/status
  verbs:
  - get
  - patch
  - update
```

**config/samples/restore_v1alpha1_filerestoreoperator.yaml:**
```yaml
apiVersion: restore.kubevirt.io/v1alpha1
kind: FileRestoreOperator
metadata:
  name: vm-file-restore-operator
  namespace: file-restore
spec:
  imagePullPolicy: IfNotPresent
  infra: {}
  workloads: {}
```

## Deployment Scenarios

### Standalone Deployment

**Workflow 1: cluster-sync**
```bash
make cluster-sync
# Deploys:
# - Operator Deployment
# - FileRestoreOperator CRD
# - VirtualMachineFileRestore CRD
# - Default FileRestoreOperator CR
# - RBAC
```

**Workflow 2: Manual**
```bash
export IMG="quay.io/agilboa/vm-file-restore-operator:latest"
make docker-build docker-push build-installer IMG=$IMG
kubectl apply -f dist/install.yaml
# install.yaml contains all resources above
```

**What happens:**
1. Operator Deployment starts
2. FileRestoreOperator controller starts watching
3. Default FileRestoreOperator CR reconciles
4. Status updates to Phase: Deployed, Available: True
5. User creates VirtualMachineFileRestore CRs as usual
6. Restore controller works exactly as before

### HCO Deployment

**HCO workflow:**
```bash
# HCO calls csv-generator
csv-generator --csv-version=1.0.0 --operator-image=<image> --dump-crds > bundle.yaml

# HCO creates FileRestoreOperator CR
kubectl apply -f - <<EOF
apiVersion: restore.kubevirt.io/v1alpha1
kind: FileRestoreOperator
metadata:
  name: vm-file-restore-operator
  namespace: kubevirt-hyperconverged
spec:
  imagePullPolicy: IfNotPresent
  infra:
    nodePlacement:
      nodeSelector:
        node-role.kubernetes.io/infra: ""
  tlsSecurityProfile:
    type: Intermediate
EOF

# FileRestoreOperator controller reconciles
# Operator becomes available
# Users create VirtualMachineFileRestore CRs
```

### Upgrade Path

**For existing deployments:**
```bash
# 1. Apply new CRD
kubectl apply -f config/crd/bases/restore.kubevirt.io_filerestoreoperators.yaml

# 2. Create default FileRestoreOperator CR
kubectl apply -f config/samples/restore_v1alpha1_filerestoreoperator.yaml

# 3. Upgrade operator image
kubectl set image deployment/vm-file-restore-operator-controller-manager \
  manager=quay.io/kubevirt/vm-file-restore-operator:v1.0.0
```

**Backwards compatibility:**
- Old install.yaml (without FileRestoreOperator CR) still works
- VirtualMachineFileRestore CRs continue working with or without FileRestoreOperator CR
- FileRestoreOperator controller gracefully handles missing CR (nothing to reconcile)

## Testing Strategy

### Unit Tests

**FileRestoreOperator Controller:**
```go
// internal/controller/filerestoreoperator_controller_test.go
func TestFileRestoreOperatorController(t *testing.T) {
    // Test reconcile updates status
    // Test Phase transitions
    // Test Condition updates
    // Test ObservedGeneration tracking
}
```

**CSV Generator:**
```go
// tools/csv-generator/csv_test.go
func TestCSVGeneration(t *testing.T) {
    // Test CSV structure
    // Test CRD embedding
    // Test RBAC rules
    // Test output format
}
```

**TLS Configuration:**
```go
// pkg/resources/utils/tls_test.go
func TestTLSConfiguration(t *testing.T) {
    // Test profile parsing
    // Test MinVersion mapping
    // Test CipherSuite parsing
}
```

### E2E Tests

**Add to existing e2e suite:**
```go
// test/e2e/e2e_test.go
var _ = Describe("FileRestoreOperator", func() {
    It("should create default FileRestoreOperator CR", func() {
        // Verify CR exists
        // Verify status is Deployed
        // Verify Available condition
    })
    
    It("should perform restore with FileRestoreOperator present", func() {
        // Existing restore tests continue to work
    })
})
```

### Manual Testing Checklist

- [ ] Standalone deployment via `make cluster-sync`
- [ ] Verify FileRestoreOperator CR created and reconciled
- [ ] Create VirtualMachineFileRestore CR
- [ ] Verify restore completes successfully
- [ ] Test TLS configuration by setting tlsSecurityProfile
- [ ] Verify csv-generator outputs valid CSV
- [ ] Test upgrade from old version without FileRestoreOperator CR
- [ ] Verify backwards compatibility (old install.yaml still works)

## What We're NOT Doing (Deferred)

### Certificate Rotation

**Why defer:**
- No webhooks exist in current operator
- Certificate rotation only needed for webhook TLS
- Migration-operator uses cert-manager because it has conversion webhooks

**When to add:**
- Only if/when validating/mutating/conversion webhooks are added
- Use cert-manager (matches existing HCO-managed operators pattern)
- OR use library-go/pkg/operator/certrotation (matches AAQ/CDI pattern)

**Where it would go:**
- cert-manager annotations in webhook configurations
- OR certrotation.go + certrotation_test.go (if using library-go)

### Operator-Level CR Actually Controlling Behavior

**Phase 1 (this design):**
- FileRestoreOperator CR exists
- Controller updates status only
- Spec fields defined but not acted upon

**Phase 2 (future):**
- Read spec.Infra and apply node placement to operator pod
- Read spec.Workloads and configure restore operation resources
- Read spec.ImagePullPolicy and apply to deployments
- Manage operator deployment updates based on spec changes

**Rationale:**
- Get HCO integration working first
- Add behavior incrementally
- Prove the pattern before building out full functionality

### Webhooks

**Not needed for:**
- Current core functionality
- HCO integration
- OLM deployment

**Could add in future for:**
- VirtualMachineFileRestore CR validation
- Defaulting sourcePath, targetPath
- Preventing unsafe configurations

### Multi-version API

**Current:**
- Everything in v1alpha1
- Single API version for both CRs

**Future:**
- Promote to v1beta1 when API stabilizes
- Add conversion webhooks if breaking changes needed
- Eventually v1 when fully stable

## Success Criteria

### Standalone Deployment Works

- ✅ `make cluster-sync` deploys operator successfully
- ✅ Default FileRestoreOperator CR created automatically
- ✅ FileRestoreOperator status shows Phase: Deployed, Available: True
- ✅ VirtualMachineFileRestore CRs work exactly as before
- ✅ No user-facing behavior changes
- ✅ No breaking changes to existing workflows

### HCO Integration Ready

- ✅ CSV generator outputs valid ClusterServiceVersion
- ✅ CSV includes both FileRestoreOperator and VirtualMachineFileRestore CRDs
- ✅ CSV includes correct RBAC rules
- ✅ FileRestoreOperator CR can be created by HCO
- ✅ TLS configuration can be set via FileRestoreOperator spec
- ✅ Status reporting follows controller-lifecycle-operator-sdk patterns
- ✅ Pattern matches HCO requirements

### Backwards Compatibility

- ✅ Old install.yaml (without FileRestoreOperator CR) still works
- ✅ Existing VirtualMachineFileRestore CRs continue working
- ✅ FileRestoreOperator controller handles missing CR gracefully
- ✅ Upgrade path from old version is smooth

### Code Quality

- ✅ Unit tests for FileRestoreOperator controller
- ✅ Unit tests for CSV generator
- ✅ E2E tests pass with new CR
- ✅ Follows HCO integration patterns
- ✅ Uses controller-lifecycle-operator-sdk properly
- ✅ Documentation updated

## Migration from Current State

### Current State
- Single CR: VirtualMachineFileRestore
- Single controller: VirtualMachineFileRestore controller
- Standalone deployment only
- No OLM support

### After Implementation
- Two CRs: FileRestoreOperator + VirtualMachineFileRestore
- Two controllers: FileRestoreOperator controller + VirtualMachineFileRestore controller
- Standalone + HCO deployment
- OLM support via CSV generator

### User Impact

**Standalone users:**
- No changes required
- Existing install.yaml continues working
- New install.yaml includes FileRestoreOperator CR automatically
- VirtualMachineFileRestore CRs work the same

**HCO users:**
- Can now manage vm-file-restore-operator via HyperConverged CR
- TLS configuration via FileRestoreOperator spec
- Unified upgrade/lifecycle management

## References

- controller-lifecycle-operator-sdk: https://github.com/kubevirt/controller-lifecycle-operator-sdk
- HCO: https://github.com/kubevirt/hyperconverged-cluster-operator
- Existing docs:
  - `docs/HCO_REQUIREMENTS_ANALYSIS.md`
  - `docs/HCO_INTEGRATION.md`
  - `docs/CERTIFICATE_ROTATION.md`

## Next Steps

After design approval:
1. Create implementation plan using writing-plans skill
2. Implement FileRestoreOperator CR and controller
3. Implement CSV generator
4. Add TLS configuration
5. Update Dockerfile and Makefile
6. Add tests
7. Update documentation
8. Test standalone deployment
9. Coordinate with HCO team for integration testing
