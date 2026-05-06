# HCO Integration Requirements - Analysis of Reference Operators

## Requirements from HCO Maintainer

Based on discussion with HCO maintainer, the vm-file-restore-operator needs:

1. **operator-sdk** - use operator-sdk for scaffolding (not just Kubebuilder)
2. **CSV generator** - binary in operator image that writes CSV and CRDs to stdout
3. **Certificate rotation** - automated cert renewal
4. **TLS configuration** - minTLSVersion + ciphers (+ curves in OpenShift 5.1)

---

## Analysis of Reference Operators

### 1. SSP Operator (kubevirt/ssp-operator)

**Framework:**
- ✅ Uses Kubebuilder v3 (see PROJECT file)
- ✅ Has operator-sdk bundle generation in Makefile
- **Both Kubebuilder AND operator-sdk are used together**

**CSV Generator:**
- ✅ Has `hack/csv-generator.go` - custom CSV generator
- ✅ Dockerfile builds csv-generator binary:
  ```dockerfile
  COPY hack/csv-generator.go hack/csv-generator.go
  RUN make manager csv-generator
  ```
- ✅ CSV generator writes to stdout (HCO requirement)
- Uses operator-framework APIs to generate ClusterServiceVersion

**Certificate Rotation:**
- ❌ No explicit cert rotation code found
- Uses webhook certificates but appears to rely on cert-manager

**TLS Configuration:**
- ✅ main.go configures TLS:
  ```go
  cfg.CipherSuites = nil
  cfg.CipherSuites = common.CipherIDs(tlsProfile.Custom.Ciphers, &ctrl.Log)
  cfg.CipherSuites = common.CipherIDs(ocpconfigv1.TLSProfiles[tlsProfile.Type].Ciphers, &ctrl.Log)
  ```
- ✅ Supports TLS profiles from OpenShift config

---

### 2. AAQ Operator (kubevirt/application-aware-quota)

**Framework:**
- ❌ No PROJECT file found - custom structure
- Uses custom Makefile, not standard Kubebuilder/operator-sdk scaffolding

**CSV Generator:**
- ✅ Has `tools/csv-generator/csv-generator.go`
- ✅ Generates ClusterServiceVersion with flags for:
  - csv-version, replaces-csv-version
  - operator-image, controller-image, aaq-server-image
  - namespace, pull-policy
  - dump-crds, dump-network-policies
- ✅ Uses operator-framework APIs

**Certificate Rotation:**
- ✅ **YES! Uses OpenShift library-go cert rotation**:
  ```go
  import "github.com/openshift/library-go/pkg/operator/certrotation"
  
  sr := certrotation.RotatedSigningCASecret{...}
  br := certrotation.CABundleConfigMap{...}
  ```
- ✅ Has dedicated certrotation.go with rotation logic
- ✅ Has certrotation_test.go with tests

**TLS Configuration:**
- ✅ Uses TLS types from vendored kubevirt.io APIs
- MinTLSVersion and CipherSuites available in types

---

### 3. CDI Operator (kubevirt/containerized-data-importer)

**Framework:**
- ❌ No PROJECT file - custom structure

**CSV Generator:**
- ✅ Has `tools/csv-generator/csv-generator.go`
- Similar structure to AAQ

**Certificate Rotation:**
- Not checked in detail

**TLS Configuration:**
- Uses vendored types with TLS security profiles

---

## Summary: What Reference Operators Actually Use

| Requirement | SSP | AAQ | CDI | Pattern |
|-------------|-----|-----|-----|---------|
| **operator-sdk** | Bundle gen only | No | No | Mixed - not all use it for scaffolding |
| **Kubebuilder** | ✅ v3 | ❌ Custom | ❌ Custom | SSP uses it, others custom |
| **CSV generator** | ✅ | ✅ | ✅ | **ALL have custom CSV generator binary** |
| **Cert rotation** | ❌ | ✅ library-go | ? | **AAQ uses OpenShift library-go** |
| **TLS config** | ✅ | ✅ | ✅ | **ALL implement TLS configuration** |

---

## Key Findings

### 1. Framework Choice
- **SSP uses Kubebuilder v3** for scaffolding, then adds operator-sdk for bundle generation
- **AAQ and CDI** use custom structures without Kubebuilder
- **operator-sdk is NOT strictly required for scaffolding** - it's mainly used for OLM bundle generation

### 2. CSV Generator (CRITICAL)
- **ALL operators have a custom CSV generator**
- Built as separate binary in operator image
- Located in `hack/csv-generator.go` or `tools/csv-generator/csv-generator.go`
- Uses `github.com/operator-framework/api/pkg/operators/v1alpha1` to generate CSV
- Writes to stdout as HCO maintainer specified
- Dockerfile includes: `LABEL org.kubevirt.hco.csv-generator.v1="/csv-generator"`

### 3. Certificate Rotation (IMPORTANT)
- **AAQ implements it** using `github.com/openshift/library-go/pkg/operator/certrotation`
- SSP appears to rely on external cert-manager
- **Pattern:** Use OpenShift's library-go for cert rotation

### 4. TLS Configuration (REQUIRED)
- **ALL operators configure TLS**
- Supports minTLSVersion and CipherSuites
- Reads TLS profiles from OpenShift config (ocpconfigv1.TLSProfiles)
- Applied to webhook servers and metrics endpoints

---

## Recommendations for vm-file-restore-operator

### Keep Current Approach:
✅ **Kubebuilder v4** - SSP proves this is acceptable
✅ **Current structure** - matches SSP pattern

### Must Add:

1. **CSV Generator** (HIGH PRIORITY)
   - Create `hack/csv-generator.go` or `tools/csv-generator/csv-generator.go`
   - Build as separate binary in Dockerfile
   - Add Dockerfile label: `LABEL org.kubevirt.hco.csv-generator.v1="/csv-generator"`
   - Generate ClusterServiceVersion and write to stdout
   - Use flags for version, images, namespace

2. **Certificate Rotation** (HIGH PRIORITY)
   - Import `github.com/openshift/library-go/pkg/operator/certrotation`
   - Implement rotation for webhook certificates
   - Add certrotation.go following AAQ pattern
   - Add tests (certrotation_test.go)

3. **TLS Configuration** (MEDIUM PRIORITY)
   - Add TLS profile support to main.go
   - Configure minTLSVersion and CipherSuites
   - Support OpenShift TLS profiles (Modern, Intermediate, Old, Custom)
   - Apply to metrics server and webhooks

4. **operator-sdk Bundle Generation** (MEDIUM PRIORITY)
   - Add bundle generation targets to Makefile
   - Use operator-sdk for OLM bundle creation
   - Not required for core operator functionality

---

## Next Steps

1. Examine AAQ's `tools/csv-generator/csv-generator.go` in detail
2. Examine AAQ's `pkg/aaq-operator/certrotation.go` in detail
3. Examine SSP's TLS configuration in main.go
4. Create implementation plan for each component
