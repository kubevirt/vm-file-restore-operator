# Certificate Rotation in KubeVirt Operators

## What is Certificate Rotation?

In the KubeVirt ecosystem, **certificate rotation** refers to automatically renewing TLS certificates used for **webhooks** and **API server** communications before they expire.

### Why It Matters

Operators that expose webhooks (validating/mutating admission webhooks, conversion webhooks) need TLS certificates to secure webhook communications with the Kubernetes API server. Without rotation:
- Certificates expire (typically 1 year)
- Webhooks stop working
- Manual intervention required to renew

**In production OpenShift/KubeVirt deployments**, HCO (HyperConverged Cluster Operator) expects all managed operators to handle cert rotation automatically.

## How KubeVirt Operators Implement It

Based on analysis of reference operators (AAQ, SSP, CDI):

### Pattern 1: OpenShift library-go (AAQ, CDI)

**Used by:** AAQ, CDI operators

**Library:** `github.com/openshift/library-go/pkg/operator/certrotation`

**How it works:**
```go
import "github.com/openshift/library-go/pkg/operator/certrotation"

// In controller setup:
sr := certrotation.RotatedSigningCASecret{
    Namespace: "kubevirt",
    Name:      "vm-file-restore-ca",
    Validity:  365 * 24 * time.Hour, // 1 year
    Refresh:   90 * 24 * time.Hour,  // Renew at 90 days before expiry
    // ...
}

br := certrotation.CABundleConfigMap{
    Namespace: "kubevirt",
    Name:      "vm-file-restore-cabundle",
    // ...
}

// In reconcile loop:
certrotation.EnsureRotatedCertificate(ctx, sr, br)
```

**What it does:**
1. Creates/maintains a CA (Certificate Authority) certificate in a Secret
2. Automatically rotates CA before expiry (at 90 days before expiration by default)
3. Creates/maintains a ConfigMap with CA bundle for webhook configuration
4. Generates server certificates signed by the CA
5. Injects CA bundle into webhook configurations

### Pattern 2: cert-manager (SSP)

**Used by:** SSP operator

**External dependency:** Requires cert-manager to be installed in cluster

**How it works:**
- Annotate webhook configurations with cert-manager annotations
- cert-manager watches and automatically provisions certificates
- Simpler but requires external dependency

## Our Operator: Current State

**vm-file-restore-operator currently has:**
- ❌ No webhooks (no validation, no conversion, no mutation)
- ❌ No TLS certificates needed
- ❌ No cert rotation needed

**We only use SSH:**
- SSH keypair managed by operator (stored in Secret)
- ED25519 keys don't expire
- No cert rotation needed for SSH

## What Minimally Needs to Be Added for HCO Integration

Based on HCO requirements, we need:

### 1. Certificate Rotation (When We Add Webhooks)

**IF** we add webhooks in the future:

```
Dependencies to add:
├── github.com/openshift/library-go/pkg/operator/certrotation
└── Required for webhook TLS cert management

Files to create:
├── internal/controller/certrotation.go
│   └── Setup rotated CA and cert rotation
├── internal/controller/certrotation_test.go
│   └── Test cert rotation logic
└── cmd/main.go (update)
    └── Initialize cert rotation in manager setup

Kubernetes resources created:
├── Secret: <operator>-ca (CA certificate)
├── Secret: <operator>-server-cert (Server certificate)
└── ConfigMap: <operator>-cabundle (CA bundle for injection)
```

**Example from AAQ:**
```go
// certrotation.go
func SetupCertRotation(ctx context.Context, mgr manager.Manager) error {
    namespace := "file-restore"
    
    // CA Secret for signing webhook certs
    caSecret := certrotation.RotatedSigningCASecret{
        Namespace: namespace,
        Name:      "vm-file-restore-ca",
        Validity:  365 * 24 * time.Hour,
        Refresh:   90 * 24 * time.Hour,
        // Informer, Client, EventRecorder...
    }
    
    // CA Bundle ConfigMap (injected into webhook configs)
    caBundle := certrotation.CABundleConfigMap{
        Namespace: namespace,
        Name:      "vm-file-restore-cabundle",
        // Informer, Client, EventRecorder...
    }
    
    // Server Cert Secret (used by webhook server)
    serverCert := certrotation.RotatedServerCert{
        Namespace: namespace,
        Name:      "vm-file-restore-server-cert",
        Validity:  30 * 24 * time.Hour,
        Refresh:   15 * 24 * time.Hour,
        CertCreator: &certrotation.ServingRotation{
            Hostnames: []string{
                "vm-file-restore-operator-webhook",
                "vm-file-restore-operator-webhook.file-restore.svc",
            },
        },
        // Informer, Client, EventRecorder...
    }
    
    return certrotation.EnsureRotatedCertificates(ctx, caSecret, caBundle, serverCert)
}
```

### 2. TLS Configuration

Even without webhooks, HCO expects TLS settings support:

```go
// cmd/main.go - add TLS profile configuration
import (
    ocpconfigv1 "github.com/openshift/api/config/v1"
)

// Configure TLS from OpenShift TLS profile
func configureTLS(mgr ctrl.Manager, tlsProfile *ocpconfigv1.TLSSecurityProfile) {
    cfg := mgr.GetWebhookServer().TLSConfig
    
    if tlsProfile != nil {
        if tlsProfile.Type == ocpconfigv1.TLSProfileCustomType {
            cfg.MinVersion = common.TLSVersion(tlsProfile.Custom.MinTLSVersion)
            cfg.CipherSuites = common.CipherIDs(tlsProfile.Custom.Ciphers)
        } else {
            profile := ocpconfigv1.TLSProfiles[tlsProfile.Type]
            cfg.MinVersion = common.TLSVersion(profile.MinTLSVersion)
            cfg.CipherSuites = common.CipherIDs(profile.Ciphers)
        }
    }
}
```

### 3. CSV Generator (For OLM)

HCO requires operators to have a CSV generator binary:

```
Files to create:
├── hack/csv-generator.go
│   └── Binary that outputs ClusterServiceVersion to stdout
└── Dockerfile (update)
    └── Build csv-generator binary in operator image

Example:
$ operator-image csv-generator --csv-version=1.0.0 --namespace=kubevirt
# Outputs CSV YAML to stdout for HCO to consume
```

## Summary: What We Need NOW vs LATER

### NOW (for basic HCO compatibility):
- ✅ **Nothing!** We have no webhooks, no TLS requirements

### LATER (when adding webhooks):
1. **Certificate Rotation**
   - Add `library-go/pkg/operator/certrotation` dependency
   - Create `certrotation.go` + tests
   - Setup CA + server cert rotation in main.go

2. **TLS Configuration**
   - Add OpenShift config API dependency
   - Support TLS profiles (minTLSVersion, cipherSuites)
   - Configure webhook server TLS

3. **CSV Generator** (for OLM integration)
   - Create `hack/csv-generator.go`
   - Build csv-generator binary in Dockerfile
   - Output ClusterServiceVersion to stdout

### Our SSH Keys

**SSH keypair rotation is SEPARATE from TLS cert rotation:**
- Not part of HCO "certificate rotation" requirement (that's for TLS/webhooks)
- SSH keys are ED25519, **don't have expiration dates** (unlike TLS certs)
- Currently stored in Secret, managed by operator
- **HCO does not require SSH key rotation** (not a standard requirement)

**However, SSH key rotation is a SECURITY BEST PRACTICE:**

Current design has security weaknesses:
- ❌ Single global keypair shared across ALL VMs
- ❌ If Secret compromised → attacker has SSH to entire fleet
- ❌ Keys stay in VM authorized_keys forever (never cleaned up)
- ❌ No automatic rotation or expiration

**Better approaches for future consideration:**

1. **Per-VM ephemeral keys** (best isolation)
   - Generate unique keypair per restore operation
   - Delete after restore completes
   - Each VM only trusts its own key

2. **SSH certificates with TTL** (best security)
   - Create SSH CA once
   - Issue short-lived certificates (1 hour validity)
   - Automatic expiration, no cleanup needed
   - Requires SSH certificate support in VMs

3. **Periodic key rotation** (minimum improvement)
   - Rotate global keypair every 90 days
   - Roll new key to VMs with grace period
   - Better than never rotating, but still a global key

**Current protection:**
- Kubernetes RBAC on Secret (only operator can read private key)
- Namespaced Secret (operator namespace only)
- Risk acceptable for trusted private clouds
- Consider rotation for multi-tenant or compliance-heavy environments

## References

- AAQ operator certrotation: https://github.com/kubevirt/application-aware-quota
- library-go cert rotation: https://github.com/openshift/library-go/tree/master/pkg/operator/certrotation
- HCO integration patterns: docs/HCO_INTEGRATION.md
- HCO requirements analysis: docs/HCO_REQUIREMENTS_ANALYSIS.md
