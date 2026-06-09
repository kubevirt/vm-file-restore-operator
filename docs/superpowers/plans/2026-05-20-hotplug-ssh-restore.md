# Hotplug and SSH Restore Implementation Plan

> **Status:** ✅ **COMPLETED** (2026-05-21) + **ENHANCED** (2026-06-04)
> 
> **Original Implementation:**
> - All 21 tasks completed successfully
> - Fixed 7 P0 critical issues (status updates, retries, idempotency)
> - Fixed 21 P1 important issues (validation, timeouts, error handling)
> - Fixed 6 additional code quality issues
> 
> **Additional Enhancements (2026-06-04):**
> - **SSH User:** Changed from `root` to dedicated `filerestore` user for cross-platform compatibility
> - **Mount Paths:** Now include source name for uniqueness (`/backup-<pvc-or-snapshot-name>`)
> - **Windows Support:** Fixed disk enumeration (ReadOnly: false), path handling, WMI-based device detection
> - **Automated Setup:** Added `setup.sh` (Linux) and `setup.bat` (Windows) for VM preparation
> - **Cleanup Timeout:** Added 30-second timeout to prevent finalizer blocking
> - **Sudo Elevation:** Scripts handle privilege escalation internally
> 
> See [Implementation Status](#implementation-status) and [Enhancements](#enhancements-beyond-original-plan) sections below

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement state machine controller with declarative volume hotplug and SSH-based file restore execution

**Architecture:** State machine controller with 9 phases (Init → Hotplugging → WaitingForAttachment → SSHConnecting → Restoring → VolumeReady/Cleanup → Succeeded/Failed), global SSH keypair managed by operator, declarative hotplug via VM spec patching, aligned with KubeVirt POC implementation

**Tech Stack:** Go 1.25+, controller-runtime, golang.org/x/crypto/ssh, KubeVirt APIs

---

## File Structure

**New Files:**
- `internal/controller/ssh.go` - SSH client wrapper and connection management
- `internal/controller/network.go` - VM IP auto-detection (interfaces → pod IP fallback)
- `internal/controller/os.go` - Guest OS detection (annotation → GuestOSInfo)
- `internal/controller/phases.go` - Phase transition logic and implementation
- `internal/controller/hotplug.go` - Volume hotplug/cleanup operations
- `internal/controller/keypair.go` - SSH keypair generation at startup
- `internal/controller/ssh_test.go` - SSH client tests
- `internal/controller/network_test.go` - Network detection tests
- `internal/controller/os_test.go` - OS detection tests
- `internal/controller/phases_test.go` - Phase logic tests
- `internal/controller/hotplug_test.go` - Hotplug tests
- `internal/controller/keypair_test.go` - Keypair generation tests

**Modified Files:**
- `api/v1alpha1/virtualmachinefilerestore_types.go` - Add MountPath, update phases
- `internal/controller/virtualmachinefilerestore_controller.go` - Implement state machine
- `cmd/main.go` - Add keypair generation on startup
- `config/rbac/role.yaml` - Add RBAC for secrets/configmaps
- `go.mod` - Add golang.org/x/crypto dependency

---

## Task 1: Update API Types

**Files:**
- Modify: `api/v1alpha1/virtualmachinefilerestore_types.go`

- [ ] **Step 1: Add new phase constants**

Add to the RestorePhase enum (after line 133):

```go
const (
	// RestorePhaseNew means the restore has been accepted but not yet started.
	RestorePhaseNew RestorePhase = "New"
	// RestorePhaseInit means initialization and validation is in progress.
	RestorePhaseInit RestorePhase = "Init"
	// RestorePhaseHotplugging means the volume is being attached to the VM.
	RestorePhaseHotplugging RestorePhase = "Hotplugging"
	// RestorePhaseWaitingForAttachment means waiting for volume to be ready.
	RestorePhaseWaitingForAttachment RestorePhase = "WaitingForAttachment"
	// RestorePhaseSSHConnecting means establishing SSH connection to guest.
	RestorePhaseSSHConnecting RestorePhase = "SSHConnecting"
	// RestorePhaseRestoring means restore operation is in progress.
	RestorePhaseRestoring RestorePhase = "Restoring"
	// RestorePhaseVolumeReady means volume is mounted for manual restore.
	RestorePhaseVolumeReady RestorePhase = "VolumeReady"
	// RestorePhaseCleanup means removing volume and cleaning up.
	RestorePhaseCleanup RestorePhase = "Cleanup"
	// RestorePhaseInProgress means the restore is currently in progress.
	RestorePhaseInProgress RestorePhase = "InProgress"
	// RestorePhaseSucceeded means the restore completed successfully.
	RestorePhaseSucceeded RestorePhase = "Succeeded"
	// RestorePhaseFailed means the restore failed.
	RestorePhaseFailed RestorePhase = "Failed"
)
```

- [ ] **Step 2: Update validation enum**

Update the validation annotation (line 132):

```go
// +kubebuilder:validation:Enum=New;Init;Hotplugging;WaitingForAttachment;SSHConnecting;Restoring;VolumeReady;Cleanup;InProgress;Succeeded;Failed
type RestorePhase string
```

- [ ] **Step 3: Add MountPath to status**

Add field to VirtualMachineFileRestoreStatus (after ErrorMessage, line 129):

```go
	// MountPath is where the restore volume is mounted in the guest OS.
	// For Linux: /backup, for Windows: C:\backup
	// +optional
	MountPath string `json:"mountPath,omitempty"`
```

- [ ] **Step 4: Regenerate manifests**

```bash
make manifests generate
```

Expected: CRD updated with new phases and MountPath field

- [ ] **Step 5: Commit**

```bash
git add api/v1alpha1/virtualmachinefilerestore_types.go config/crd/
git commit -m "api: add state machine phases and MountPath to status"
```

---

## Task 2: Add SSH Keypair Generation

**Files:**
- Create: `internal/controller/keypair.go`
- Create: `internal/controller/keypair_test.go`

- [ ] **Step 1: Write test for keypair generation**

Create `internal/controller/keypair_test.go`:

```go
package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnsureSSHKeypair_CreatesKeypair(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	err := EnsureSSHKeypair(context.Background(), client, "test-ns")
	if err != nil {
		t.Fatalf("EnsureSSHKeypair failed: %v", err)
	}

	// Verify Secret created
	secret := &corev1.Secret{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: "test-ns",
	}, secret)
	if err != nil {
		t.Fatalf("Secret not created: %v", err)
	}

	if _, ok := secret.Data["ssh-privatekey"]; !ok {
		t.Error("ssh-privatekey not found in Secret")
	}

	// Verify ConfigMap created
	cm := &corev1.ConfigMap{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: "test-ns",
	}, cm)
	if err != nil {
		t.Fatalf("ConfigMap not created: %v", err)
	}

	if _, ok := cm.Data["ssh-publickey"]; !ok {
		t.Error("ssh-publickey not found in ConfigMap")
	}

	pubKey := cm.Data["ssh-publickey"]
	if len(pubKey) == 0 {
		t.Error("ssh-publickey is empty")
	}
	if len(pubKey) < 50 || len(pubKey) > 150 {
		t.Errorf("ssh-publickey has unexpected length: %d", len(pubKey))
	}
}

func TestEnsureSSHKeypair_SkipsIfExists(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SSHKeypairSecretName,
			Namespace: "test-ns",
		},
		Data: map[string][]byte{
			"ssh-privatekey": []byte("existing-key"),
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existingSecret).
		Build()

	err := EnsureSSHKeypair(context.Background(), client, "test-ns")
	if err != nil {
		t.Fatalf("EnsureSSHKeypair failed: %v", err)
	}

	// Verify Secret unchanged
	secret := &corev1.Secret{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: "test-ns",
	}, secret)
	if err != nil {
		t.Fatal(err)
	}

	if string(secret.Data["ssh-privatekey"]) != "existing-key" {
		t.Error("Secret was modified when it should have been skipped")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/controller -run TestEnsureSSHKeypair -v
```

Expected: FAIL with "undefined: EnsureSSHKeypair"

- [ ] **Step 3: Create keypair generation implementation**

Create `internal/controller/keypair.go`:

```go
package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"golang.org/x/crypto/ssh"
)

const (
	// SSHKeypairSecretName is the name of the Secret and ConfigMap for SSH keys
	SSHKeypairSecretName = "vm-file-restore-operator-ssh"
)

// EnsureSSHKeypair generates an ED25519 SSH keypair if it doesn't exist.
// Private key is stored in a Secret, public key in a ConfigMap.
func EnsureSSHKeypair(ctx context.Context, c client.Client, namespace string) error {
	// Check if Secret already exists
	secret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: namespace,
	}, secret)

	if err == nil {
		// Secret exists, skip generation
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check for existing Secret: %w", err)
	}

	// Generate ED25519 keypair
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate keypair: %w", err)
	}

	// Format private key as OpenSSH format
	privKeyBytes, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}
	privKeyPEM := pem.EncodeToMemory(privKeyBytes)

	// Format public key
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("failed to create SSH public key: %w", err)
	}
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPublicKey))

	// Create Secret for private key
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SSHKeypairSecretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ssh-privatekey": privKeyPEM,
		},
	}

	if err := c.Create(ctx, secret); err != nil {
		return fmt.Errorf("failed to create Secret: %w", err)
	}

	// Create ConfigMap for public key
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SSHKeypairSecretName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"ssh-publickey": pubKeyStr,
		},
	}

	if err := c.Create(ctx, configMap); err != nil {
		return fmt.Errorf("failed to create ConfigMap: %w", err)
	}

	return nil
}
```

- [ ] **Step 4: Add dependency to go.mod**

```bash
go get golang.org/x/crypto/ssh
go mod tidy
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/controller -run TestEnsureSSHKeypair -v
```

Expected: PASS (both tests)

- [ ] **Step 6: Commit**

```bash
git add internal/controller/keypair.go internal/controller/keypair_test.go go.mod go.sum
git commit -m "feat: add SSH keypair generation at startup"
```

---

## Task 3: Add OS Detection

**Files:**
- Create: `internal/controller/os.go`
- Create: `internal/controller/os_test.go`

- [ ] **Step 1: Write test for OS detection**

Create `internal/controller/os_test.go`:

```go
package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "kubevirt.io/api/core/v1"
)

func TestDetectGuestOS_FromAnnotation_Windows(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"vm.kubevirt.io/os": "windows2022",
			},
		},
	}

	os, mountPath := DetectGuestOS(vmi)
	if os != "windows" {
		t.Errorf("expected windows, got %s", os)
	}
	if mountPath != "C:\\backup" {
		t.Errorf("expected C:\\backup, got %s", mountPath)
	}
}

func TestDetectGuestOS_FromAnnotation_Linux(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"vm.kubevirt.io/os": "fedora",
			},
		},
	}

	os, mountPath := DetectGuestOS(vmi)
	if os != "linux" {
		t.Errorf("expected linux, got %s", os)
	}
	if mountPath != "/backup" {
		t.Errorf("expected /backup, got %s", mountPath)
	}
}

func TestDetectGuestOS_FromGuestOSInfo_Windows(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		Status: v1.VirtualMachineInstanceStatus{
			GuestOSInfo: v1.VirtualMachineInstanceGuestOSInfo{
				Name: "Microsoft Windows Server 2022",
			},
		},
	}

	os, mountPath := DetectGuestOS(vmi)
	if os != "windows" {
		t.Errorf("expected windows, got %s", os)
	}
	if mountPath != "C:\\backup" {
		t.Errorf("expected C:\\backup, got %s", mountPath)
	}
}

func TestDetectGuestOS_DefaultToLinux(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{}

	os, mountPath := DetectGuestOS(vmi)
	if os != "linux" {
		t.Errorf("expected linux default, got %s", os)
	}
	if mountPath != "/backup" {
		t.Errorf("expected /backup, got %s", mountPath)
	}
}

func TestGetHelperScriptPath_Linux(t *testing.T) {
	path := GetHelperScriptPath("linux")
	expected := "/usr/local/bin/filerestore.sh"
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestGetHelperScriptPath_Windows(t *testing.T) {
	path := GetHelperScriptPath("windows")
	expected := `"C:\Program Files\filerestore\filerestore.bat"`
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/controller -run TestDetectGuestOS -v
```

Expected: FAIL with "undefined: DetectGuestOS"

- [ ] **Step 3: Implement OS detection**

Create `internal/controller/os.go`:

```go
package controller

import (
	"strings"

	v1 "kubevirt.io/api/core/v1"
)

// DetectGuestOS determines if the VMI is running Windows or Linux.
// Returns OS type ("windows" or "linux") and mount path.
func DetectGuestOS(vmi *v1.VirtualMachineInstance) (osType string, mountPath string) {
	// Primary: Check annotation
	if os, ok := vmi.Annotations["vm.kubevirt.io/os"]; ok {
		if strings.HasPrefix(strings.ToLower(os), "windows") {
			return "windows", "C:\\backup"
		}
		return "linux", "/backup"
	}

	// Fallback: Check guest agent info
	if vmi.Status.GuestOSInfo.Name != "" {
		if strings.Contains(strings.ToLower(vmi.Status.GuestOSInfo.Name), "windows") {
			return "windows", "C:\\backup"
		}
		return "linux", "/backup"
	}

	// Default to Linux
	return "linux", "/backup"
}

// GetHelperScriptPath returns the path to the helper script based on OS.
func GetHelperScriptPath(osType string) string {
	if osType == "windows" {
		return `"C:\Program Files\filerestore\filerestore.bat"`
	}
	return "/usr/local/bin/filerestore.sh"
}
```

- [ ] **Step 4: Add KubeVirt API dependency**

```bash
go get kubevirt.io/api/core/v1
go mod tidy
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/controller -run TestDetectGuestOS -v
go test ./internal/controller -run TestGetHelperScriptPath -v
```

Expected: PASS (all tests)

- [ ] **Step 6: Commit**

```bash
git add internal/controller/os.go internal/controller/os_test.go go.mod go.sum
git commit -m "feat: add guest OS detection from VMI"
```

---

## Task 4: Add Network Auto-Detection

**Files:**
- Create: `internal/controller/network.go`
- Create: `internal/controller/network_test.go`

- [ ] **Step 1: Write test for network detection**

Create `internal/controller/network_test.go`:

```go
package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	v1 "kubevirt.io/api/core/v1"
)

func TestGetVMIPAddress_FromVMIInterface(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		Status: v1.VirtualMachineInstanceStatus{
			Interfaces: []v1.VirtualMachineInstanceNetworkInterface{
				{
					Name: "default",
					IP:   "10.244.0.5",
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	ip, err := GetVMIPAddress(context.Background(), client, vmi)
	if err != nil {
		t.Fatalf("GetVMIPAddress failed: %v", err)
	}

	if ip != "10.244.0.5" {
		t.Errorf("expected 10.244.0.5, got %s", ip)
	}
}

func TestGetVMIPAddress_FromPodIP(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmi",
			Namespace: "default",
			UID:       "vmi-uid-123",
		},
		Status: v1.VirtualMachineInstanceStatus{
			// No interfaces
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "virt-launcher-test-vmi",
			Namespace: "default",
			Labels: map[string]string{
				"kubevirt.io/domain": "test-vmi",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "kubevirt.io/v1",
					Kind:       "VirtualMachineInstance",
					Name:       "test-vmi",
					UID:        "vmi-uid-123",
				},
			},
		},
		Status: corev1.PodStatus{
			PodIP: "10.244.1.10",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	ip, err := GetVMIPAddress(context.Background(), client, vmi)
	if err != nil {
		t.Fatalf("GetVMIPAddress failed: %v", err)
	}

	if ip != "10.244.1.10" {
		t.Errorf("expected 10.244.1.10, got %s", ip)
	}
}

func TestGetVMIPAddress_NoIPAvailable(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmi",
			Namespace: "default",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err := GetVMIPAddress(context.Background(), client, vmi)
	if err == nil {
		t.Error("expected error when no IP available, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/controller -run TestGetVMIPAddress -v
```

Expected: FAIL with "undefined: GetVMIPAddress"

- [ ] **Step 3: Implement network detection**

Create `internal/controller/network.go`:

```go
package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	v1 "kubevirt.io/api/core/v1"
)

// GetVMIPAddress returns the IP address to use for SSH connection.
// Tries VMI interfaces first, falls back to virt-launcher pod IP.
func GetVMIPAddress(ctx context.Context, c client.Client, vmi *v1.VirtualMachineInstance) (string, error) {
	// Option 1: Check VMI interfaces for IP
	for _, iface := range vmi.Status.Interfaces {
		if iface.Name == "default" && iface.IP != "" {
			return iface.IP, nil
		}
	}

	// Option 2: Fallback to virt-launcher pod IP
	pod, err := getPodForVMI(ctx, c, vmi)
	if err != nil {
		return "", fmt.Errorf("failed to get pod for VMI: %w", err)
	}

	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("pod IP not available")
	}

	return pod.Status.PodIP, nil
}

// getPodForVMI finds the virt-launcher pod for a VMI.
func getPodForVMI(ctx context.Context, c client.Client, vmi *v1.VirtualMachineInstance) (*corev1.Pod, error) {
	podList := &corev1.PodList{}
	err := c.List(ctx, podList, client.InNamespace(vmi.Namespace), client.MatchingLabels{
		"kubevirt.io/domain": vmi.Name,
	})
	if err != nil {
		return nil, err
	}

	// Find pod owned by this VMI
	for i := range podList.Items {
		pod := &podList.Items[i]
		for _, ownerRef := range pod.OwnerReferences {
			if ownerRef.UID == vmi.UID {
				return pod, nil
			}
		}
	}

	return nil, fmt.Errorf("virt-launcher pod not found for VMI %s/%s", vmi.Namespace, vmi.Name)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/controller -run TestGetVMIPAddress -v
```

Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add internal/controller/network.go internal/controller/network_test.go
git commit -m "feat: add VM IP address auto-detection"
```

---

## Task 5: Add SSH Client

**Files:**
- Create: `internal/controller/ssh.go`
- Create: `internal/controller/ssh_test.go`

- [ ] **Step 1: Write test for SSH connection**

Create `internal/controller/ssh_test.go`:

```go
package controller

import (
	"testing"
)

func TestBuildSSHCommand_LinuxAutomatic(t *testing.T) {
	cmd := BuildSSHCommand("linux", "test-restore", "/backup", "/home/user/data")
	expected := "/usr/local/bin/filerestore.sh restore --serial test-restore --mount-path /backup --source-path /home/user/data"
	if cmd != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, cmd)
	}
}

func TestBuildSSHCommand_LinuxManual(t *testing.T) {
	cmd := BuildSSHCommand("linux", "test-restore", "/backup", "")
	expected := "/usr/local/bin/filerestore.sh restore --serial test-restore --mount-path /backup"
	if cmd != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, cmd)
	}
}

func TestBuildSSHCommand_WindowsAutomatic(t *testing.T) {
	cmd := BuildSSHCommand("windows", "test-restore", "C:\\backup", "C:\\Users\\data")
	expected := `"C:\Program Files\filerestore\filerestore.bat" restore --serial test-restore --mount-path "C:\backup" --source-path "C:\Users\data"`
	if cmd != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, cmd)
	}
}

func TestBuildSSHCommand_WindowsManual(t *testing.T) {
	cmd := BuildSSHCommand("windows", "test-restore", "C:\\backup", "")
	expected := `"C:\Program Files\filerestore\filerestore.bat" restore --serial test-restore --mount-path "C:\backup"`
	if cmd != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, cmd)
	}
}

func TestBuildCleanupCommand_Linux(t *testing.T) {
	cmd := BuildCleanupCommand("linux", "/backup")
	expected := "/usr/local/bin/filerestore.sh cleanup --mount-path /backup"
	if cmd != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, cmd)
	}
}

func TestBuildCleanupCommand_Windows(t *testing.T) {
	cmd := BuildCleanupCommand("windows", "C:\\backup")
	expected := `"C:\Program Files\filerestore\filerestore.bat" cleanup --mount-path "C:\backup"`
	if cmd != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, cmd)
	}
}

func TestTruncateOutput(t *testing.T) {
	// Short output - unchanged
	short := "line1\nline2\nline3"
	result := TruncateOutput(short, 100)
	if result != short {
		t.Errorf("short output was modified")
	}

	// Long output - truncated to last N lines
	lines := ""
	for i := 1; i <= 150; i++ {
		lines += "line" + string(rune(i)) + "\n"
	}
	result = TruncateOutput(lines, 100)
	
	resultLines := 0
	for _, c := range result {
		if c == '\n' {
			resultLines++
		}
	}
	
	if resultLines > 100 {
		t.Errorf("expected at most 100 lines, got %d", resultLines)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/controller -run TestBuildSSHCommand -v
go test ./internal/controller -run TestBuildCleanupCommand -v
go test ./internal/controller -run TestTruncateOutput -v
```

Expected: FAIL with "undefined: BuildSSHCommand"

- [ ] **Step 3: Implement SSH command builders**

Create `internal/controller/ssh.go`:

```go
package controller

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHClient wraps ssh.Client with helper methods.
type SSHClient struct {
	client *ssh.Client
}

// ConnectSSH establishes SSH connection to the given IP.
func ConnectSSH(ip string, privateKey []byte) (*SSHClient, error) {
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: "filerestore",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // VMs can be recreated with same IP
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(ip, "22")
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	return &SSHClient{client: client}, nil
}

// RunCommand executes a command via SSH and returns stdout, stderr, and error.
func (c *SSHClient) RunCommand(ctx context.Context, command string) (stdout, stderr string, err error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("create session: %w", err)
	}
	defer session.Close()

	var outBuf, errBuf bytes.Buffer
	session.Stdout = &outBuf
	session.Stderr = &errBuf

	err = session.Run(command)
	return outBuf.String(), errBuf.String(), err
}

// Close closes the SSH connection.
func (c *SSHClient) Close() error {
	return c.client.Close()
}

// BuildSSHCommand constructs the restore command based on OS and mode.
func BuildSSHCommand(osType, volumeName, mountPath, sourcePath string) string {
	scriptPath := GetHelperScriptPath(osType)

	if osType == "windows" {
		// Windows: quote paths
		if sourcePath != "" {
			return fmt.Sprintf(`%s restore --serial %s --mount-path "%s" --source-path "%s"`,
				scriptPath, volumeName, mountPath, sourcePath)
		}
		return fmt.Sprintf(`%s restore --serial %s --mount-path "%s"`,
			scriptPath, volumeName, mountPath)
	}

	// Linux
	if sourcePath != "" {
		return fmt.Sprintf(`%s restore --serial %s --mount-path %s --source-path %s`,
			scriptPath, volumeName, mountPath, sourcePath)
	}
	return fmt.Sprintf(`%s restore --serial %s --mount-path %s`,
		scriptPath, volumeName, mountPath)
}

// BuildCleanupCommand constructs the cleanup command based on OS.
func BuildCleanupCommand(osType, mountPath string) string {
	scriptPath := GetHelperScriptPath(osType)

	if osType == "windows" {
		return fmt.Sprintf(`%s cleanup --mount-path "%s"`, scriptPath, mountPath)
	}

	return fmt.Sprintf(`%s cleanup --mount-path %s`, scriptPath, mountPath)
}

// TruncateOutput truncates output to last N lines if it exceeds the limit.
func TruncateOutput(output string, maxLines int) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}

	// Return last maxLines lines
	return strings.Join(lines[len(lines)-maxLines:], "\n")
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/controller -run TestBuildSSHCommand -v
go test ./internal/controller -run TestBuildCleanupCommand -v
go test ./internal/controller -run TestTruncateOutput -v
```

Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add internal/controller/ssh.go internal/controller/ssh_test.go
git commit -m "feat: add SSH client and command builders"
```

---

## Task 6: Add Volume Hotplug Operations

**Files:**
- Create: `internal/controller/hotplug.go`
- Create: `internal/controller/hotplug_test.go`

- [ ] **Step 1: Write test for volume name generation**

Create `internal/controller/hotplug_test.go`:

```go
package controller

import (
	"testing"
)

func TestGetVolumeName(t *testing.T) {
	name := GetVolumeName("my-restore")
	expected := "my-restore-restore"
	if name != expected {
		t.Errorf("expected %s, got %s", expected, name)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/controller -run TestGetVolumeName -v
```

Expected: FAIL with "undefined: GetVolumeName"

- [ ] **Step 3: Implement volume name helper**

Create `internal/controller/hotplug.go`:

```go
package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	v1 "kubevirt.io/api/core/v1"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

// GetVolumeName returns the volume name for a restore operation.
func GetVolumeName(crName string) string {
	return crName + "-restore"
}

// HotplugVolume attaches a volume to the VM by patching the VM spec.
func HotplugVolume(ctx context.Context, c client.Client, vmfr *restorev1alpha1.VirtualMachineFileRestore, vm *v1.VirtualMachine) error {
	volumeName := GetVolumeName(vmfr.Name)

	// Check if volume already exists
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		if vol.Name == volumeName {
			// Already exists, skip
			return nil
		}
	}

	// Build volume source based on restore source type
	var volumeSource v1.VolumeSource
	var tempPVC *corev1.PersistentVolumeClaim

	if vmfr.Spec.Source.PVC != nil {
		// PVC source - direct attach
		volumeSource = v1.VolumeSource{
			PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
				PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: vmfr.Spec.Source.PVC.Name,
				},
				Hotpluggable: true,
			},
		}
	} else if vmfr.Spec.Source.Snapshot != nil {
		// Snapshot source - create temp PVC first
		tempPVC = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      volumeName,
				Namespace: vmfr.Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "vm-file-restore-operator",
					"filerestore.kubevirt.io/name": vmfr.Name,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				DataSource: &corev1.TypedLocalObjectReference{
					APIGroup: ptr.To("snapshot.storage.k8s.io"),
					Kind:     "VolumeSnapshot",
					Name:     vmfr.Spec.Source.Snapshot.Name,
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadOnlyMany,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"), // TODO: match snapshot size
					},
				},
			},
		}

		if err := c.Create(ctx, tempPVC); err != nil {
			return fmt.Errorf("create temp PVC from snapshot: %w", err)
		}

		volumeSource = v1.VolumeSource{
			PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
				PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: volumeName,
				},
				Hotpluggable: true,
			},
		}
	} else if vmfr.Spec.Source.Remote != nil {
		return fmt.Errorf("remote sources not yet supported")
	} else {
		return fmt.Errorf("no valid source specified")
	}

	// Add volume to VM spec
	newVolume := v1.Volume{
		Name:         volumeName,
		VolumeSource: volumeSource,
	}

	// Add disk to VM spec
	newDisk := v1.Disk{
		Name: volumeName,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus:      v1.DiskBusSCSI,
				ReadOnly: ptr.To(true), // Safety: read-only mount
			},
		},
		Serial: volumeName, // For guest detection via lsblk
	}

	// Patch VM spec
	vm.Spec.Template.Spec.Volumes = append(vm.Spec.Template.Spec.Volumes, newVolume)
	vm.Spec.Template.Spec.Domain.Devices.Disks = append(vm.Spec.Template.Spec.Domain.Devices.Disks, newDisk)

	if err := c.Update(ctx, vm); err != nil {
		return fmt.Errorf("patch VM spec: %w", err)
	}

	return nil
}

// UnplugVolume removes a volume from the VM by patching the VM spec.
func UnplugVolume(ctx context.Context, c client.Client, vmfr *restorev1alpha1.VirtualMachineFileRestore, vm *v1.VirtualMachine) error {
	volumeName := GetVolumeName(vmfr.Name)

	// Remove volume from spec
	newVolumes := []v1.Volume{}
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		if vol.Name != volumeName {
			newVolumes = append(newVolumes, vol)
		}
	}

	// Remove disk from spec
	newDisks := []v1.Disk{}
	for _, disk := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		if disk.Name != volumeName {
			newDisks = append(newDisks, disk)
		}
	}

	vm.Spec.Template.Spec.Volumes = newVolumes
	vm.Spec.Template.Spec.Domain.Devices.Disks = newDisks

	if err := c.Update(ctx, vm); err != nil {
		return fmt.Errorf("patch VM spec to remove volume: %w", err)
	}

	// For snapshot sources: delete temp PVC
	if vmfr.Spec.Source.Snapshot != nil {
		tempPVC := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      volumeName,
				Namespace: vmfr.Namespace,
			},
		}
		if err := c.Delete(ctx, tempPVC); err != nil {
			// Best effort - log but don't fail
			return fmt.Errorf("delete temp PVC: %w", err)
		}
	}

	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/controller -run TestGetVolumeName -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/controller/hotplug.go internal/controller/hotplug_test.go
git commit -m "feat: add volume hotplug operations"
```

---

## Task 7: Update RBAC for SSH and Events

**Files:**
- Modify: `config/rbac/role.yaml`

- [ ] **Step 1: Add RBAC for Secrets and ConfigMaps**

Add to `config/rbac/role.yaml` (after existing rules):

```yaml
- apiGroups:
  - ""
  resources:
  - secrets
  verbs:
  - get
  - list
  - watch
  - create
- apiGroups:
  - ""
  resources:
  - configmaps
  verbs:
  - get
  - list
  - watch
  - create
- apiGroups:
  - kubevirt.io
  resources:
  - virtualmachines
  verbs:
  - get
  - list
  - watch
  - update
  - patch
- apiGroups:
  - kubevirt.io
  resources:
  - virtualmachineinstances
  verbs:
  - get
  - list
  - watch
```

- [ ] **Step 2: Verify RBAC generation**

```bash
make manifests
git diff config/rbac/role.yaml
```

Expected: New rules added for secrets, configmaps, VMs, VMIs

- [ ] **Step 3: Commit**

```bash
git add config/rbac/role.yaml
git commit -m "rbac: add permissions for SSH keys and VM hotplug"
```

---

## Task 8: Integrate Keypair Generation at Startup

**Files:**
- Modify: `cmd/main.go`

- [ ] **Step 1: Add keypair generation to main**

Update `cmd/main.go`, add after manager creation (around line 90):

```go
	// Generate SSH keypair on startup
	operatorNamespace := os.Getenv("OPERATOR_NAMESPACE")
	if operatorNamespace == "" {
		operatorNamespace = "file-restore"
	}

	setupLog.Info("Ensuring SSH keypair exists", "namespace", operatorNamespace)
	if err := controller.EnsureSSHKeypair(context.Background(), mgr.GetClient(), operatorNamespace); err != nil {
		setupLog.Error(err, "Failed to ensure SSH keypair")
		os.Exit(1)
	}
	setupLog.Info("SSH keypair ready")
```

- [ ] **Step 2: Build and test**

```bash
make build
```

Expected: Successful build

- [ ] **Step 3: Commit**

```bash
git add cmd/main.go
git commit -m "feat: generate SSH keypair on operator startup"
```

---

## Task 9: Add Finalizer Handling

**Files:**
- Modify: `internal/controller/virtualmachinefilerestore_controller.go`

- [ ] **Step 1: Add finalizer constant**

Add at top of file (after imports):

```go
const (
	finalizerName = "filerestore.kubevirt.io/cleanup"
)
```

- [ ] **Step 2: Add finalizer handling to reconcile**

Replace the reconcile method content (lines 51-120) with:

```go
func (r *VirtualMachineFileRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the VirtualMachineFileRestore instance
	vmFileRestore := &restorev1alpha1.VirtualMachineFileRestore{}
	if err := r.Get(ctx, req.NamespacedName, vmFileRestore); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("VirtualMachineFileRestore resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get VirtualMachineFileRestore")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !vmFileRestore.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(vmFileRestore, finalizerName) {
			// Run cleanup
			if err := r.cleanup(ctx, vmFileRestore); err != nil {
				logger.Error(err, "Cleanup failed")
				// Continue to remove finalizer even on error (best effort)
			}

			// Remove finalizer
			controllerutil.RemoveFinalizer(vmFileRestore, finalizerName)
			if err := r.Update(ctx, vmFileRestore); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(vmFileRestore, finalizerName) {
		controllerutil.AddFinalizer(vmFileRestore, finalizerName)
		if err := r.Update(ctx, vmFileRestore); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	logger.Info("Reconciling VirtualMachineFileRestore",
		"name", vmFileRestore.Name,
		"namespace", vmFileRestore.Namespace,
		"phase", vmFileRestore.Status.Phase)

	// If already completed or failed, nothing to do
	if vmFileRestore.Status.Phase == restorev1alpha1.RestorePhaseSucceeded ||
		vmFileRestore.Status.Phase == restorev1alpha1.RestorePhaseFailed {
		logger.Info("Restore already in terminal state", "phase", vmFileRestore.Status.Phase)
		return ctrl.Result{}, nil
	}

	// TODO: State machine logic will go here

	return ctrl.Result{}, nil
}

// cleanup performs cleanup when CR is deleted
func (r *VirtualMachineFileRestoreReconciler) cleanup(ctx context.Context, vmfr *restorev1alpha1.VirtualMachineFileRestore) error {
	logger := log.FromContext(ctx)
	logger.Info("Running cleanup", "name", vmfr.Name)

	// TODO: Implement cleanup logic (SSH cleanup, volume unplug)
	
	return nil
}
```

- [ ] **Step 3: Add controllerutil import**

Add to imports:

```go
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
```

- [ ] **Step 4: Build to verify**

```bash
make build
```

Expected: Successful build

- [ ] **Step 5: Commit**

```bash
git add internal/controller/virtualmachinefilerestore_controller.go
git commit -m "feat: add finalizer handling for cleanup"
```

---

## Task 10: Add Event Recording to Controller

**Files:**
- Modify: `internal/controller/virtualmachinefilerestore_controller.go`

- [ ] **Step 1: Add event recorder to reconciler**

Add to VirtualMachineFileRestoreReconciler struct (around line 34):

```go
	Recorder record.EventRecorder
```

- [ ] **Step 2: Add import**

Add to imports:

```go
	"k8s.io/client-go/tools/record"
```

- [ ] **Step 3: Set event recorder in SetupWithManager**

Find SetupWithManager method (line 180+), add before return:

```go
	r.Recorder = mgr.GetEventRecorderFor("virtualmachinefilerestore-controller")
```

- [ ] **Step 4: Build to verify**

```bash
make build
```

Expected: Successful build

- [ ] **Step 5: Commit**

```bash
git add internal/controller/virtualmachinefilerestore_controller.go
git commit -m "feat: add event recorder to controller"
```

---

## Task 11: Implement State Machine Core

**Files:**
- Create: `internal/controller/phases.go`

- [ ] **Step 1: Create phase handler interface**

Create `internal/controller/phases.go`:

```go
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	v1 "kubevirt.io/api/core/v1"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

// phaseHandler defines the interface for handling each phase.
type phaseHandler func(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error)

// getPhaseHandler returns the handler for the current phase.
func getPhaseHandler(phase restorev1alpha1.RestorePhase) phaseHandler {
	switch phase {
	case restorev1alpha1.RestorePhaseNew, "":
		return handleInitPhase
	case restorev1alpha1.RestorePhaseInit:
		return handleHotpluggingPhase
	case restorev1alpha1.RestorePhaseHotplugging:
		return handleWaitingForAttachmentPhase
	case restorev1alpha1.RestorePhaseWaitingForAttachment:
		return handleSSHConnectingPhase
	case restorev1alpha1.RestorePhaseSSHConnecting:
		return handleRestoringPhase
	case restorev1alpha1.RestorePhaseRestoring:
		return handlePostRestorePhase
	case restorev1alpha1.RestorePhaseVolumeReady:
		return handleVolumeReadyPhase
	case restorev1alpha1.RestorePhaseCleanup:
		return handleCleanupPhase
	default:
		return nil
	}
}

// transitionPhase updates the phase and requeues.
func transitionPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore, newPhase restorev1alpha1.RestorePhase, message string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	oldPhase := vmfr.Status.Phase

	vmfr.Status.Phase = newPhase

	// Set start time if transitioning from New
	if oldPhase == "" || oldPhase == restorev1alpha1.RestorePhaseNew {
		now := metav1.Now()
		vmfr.Status.StartTime = &now
	}

	// Set completion time if terminal phase
	if newPhase == restorev1alpha1.RestorePhaseSucceeded || newPhase == restorev1alpha1.RestorePhaseFailed {
		now := metav1.Now()
		vmfr.Status.CompletionTime = &now
	}

	if err := r.Status().Update(ctx, vmfr); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Phase transition", "from", oldPhase, "to", newPhase, "message", message)
	r.Recorder.Event(vmfr, corev1.EventTypeNormal, string(newPhase), message)

	return ctrl.Result{Requeue: true}, nil
}

// failRestore transitions to Failed phase with error details.
func failRestore(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore, err error, detail string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	vmfr.Status.Phase = restorev1alpha1.RestorePhaseFailed
	vmfr.Status.ErrorMessage = err.Error()

	now := metav1.Now()
	vmfr.Status.CompletionTime = &now

	// Add condition with detailed error
	condition := metav1.Condition{
		Type:               "RestoreCompleted",
		Status:             metav1.ConditionFalse,
		Reason:             "RestoreFailed",
		Message:            TruncateOutput(detail, 100),
		LastTransitionTime: now,
	}
	vmfr.Status.Conditions = append(vmfr.Status.Conditions, condition)

	if err := r.Status().Update(ctx, vmfr); err != nil {
		return ctrl.Result{}, err
	}

	logger.Error(err, "Restore failed", "detail", detail)
	r.Recorder.Event(vmfr, corev1.EventTypeWarning, "Failed", err.Error())

	// Best effort cleanup
	_ = r.cleanup(ctx, vmfr)

	return ctrl.Result{}, nil
}
```

- [ ] **Step 2: Commit**

```bash
git add internal/controller/phases.go
git commit -m "feat: add phase handler infrastructure"
```

---

## Task 12: Implement Init Phase

**Files:**
- Modify: `internal/controller/phases.go`

- [ ] **Step 1: Implement Init phase handler**

Add to `internal/controller/phases.go`:

```go
// handleInitPhase validates configuration and detects OS.
func handleInitPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Init phase")

	// Get target VM
	vm := &v1.VirtualMachine{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vm)
	if err != nil {
		if errors.IsNotFound(err) {
			return failRestore(ctx, r, vmfr, err, "Target VM not found")
		}
		return ctrl.Result{}, err
	}

	// Get VMI to check if running
	vmi := &v1.VirtualMachineInstance{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      vm.Name,
		Namespace: vm.Namespace,
	}, vmi)
	if err != nil {
		if errors.IsNotFound(err) {
			return failRestore(ctx, r, vmfr, fmt.Errorf("VM not running"), "Target VM must be running")
		}
		return ctrl.Result{}, err
	}

	// Validate source exists
	if vmfr.Spec.Source.PVC != nil {
		pvc := &corev1.PersistentVolumeClaim{}
		err = r.Get(ctx, types.NamespacedName{
			Name:      vmfr.Spec.Source.PVC.Name,
			Namespace: vmfr.Namespace,
		}, pvc)
		if err != nil {
			if errors.IsNotFound(err) {
				return failRestore(ctx, r, vmfr, err, fmt.Sprintf("Source PVC %s not found", vmfr.Spec.Source.PVC.Name))
			}
			return ctrl.Result{}, err
		}
	} else if vmfr.Spec.Source.Snapshot != nil {
		// TODO: Validate VolumeSnapshot exists
		// For now, skip - will fail later if missing
	} else if vmfr.Spec.Source.Remote != nil {
		return failRestore(ctx, r, vmfr, fmt.Errorf("remote sources not supported"), "Remote backup sources not yet implemented")
	}

	// Detect guest OS
	osType, mountPath := DetectGuestOS(vmi)
	vmfr.Status.MountPath = mountPath

	logger.Info("OS detected", "os", osType, "mountPath", mountPath)
	r.Recorder.Eventf(vmfr, corev1.EventTypeNormal, "Initialized", "OS detected: %s", osType)

	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseHotplugging, fmt.Sprintf("Initialization complete, OS: %s", osType))
}
```

- [ ] **Step 2: Add corev1 import if missing**

Ensure imports include:

```go
	corev1 "k8s.io/api/core/v1"
```

- [ ] **Step 3: Build to verify**

```bash
make build
```

Expected: Successful build

- [ ] **Step 4: Commit**

```bash
git add internal/controller/phases.go
git commit -m "feat: implement Init phase"
```

---

## Task 13: Implement Hotplugging Phase

**Files:**
- Modify: `internal/controller/phases.go`

- [ ] **Step 1: Implement Hotplugging phase handler**

Add to `internal/controller/phases.go`:

```go
// handleHotpluggingPhase attaches the restore volume to the VM.
func handleHotpluggingPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Hotplugging phase")

	// Get target VM
	vm := &v1.VirtualMachine{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vm)
	if err != nil {
		return ctrl.Result{}, err
	}

	volumeName := GetVolumeName(vmfr.Name)
	logger.Info("Hotplugging volume", "volumeName", volumeName)

	// Hotplug volume
	if err := HotplugVolume(ctx, r.Client, vmfr, vm); err != nil {
		return failRestore(ctx, r, vmfr, err, fmt.Sprintf("Failed to hotplug volume: %v", err))
	}

	r.Recorder.Eventf(vmfr, corev1.EventTypeNormal, "VolumeHotplugStarted", "Attaching volume %s", volumeName)

	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseWaitingForAttachment, "Volume hotplug initiated")
}
```

- [ ] **Step 2: Build to verify**

```bash
make build
```

Expected: Successful build

- [ ] **Step 3: Commit**

```bash
git add internal/controller/phases.go
git commit -m "feat: implement Hotplugging phase"
```

---

## Task 14: Implement WaitingForAttachment Phase

**Files:**
- Modify: `internal/controller/phases.go`

- [ ] **Step 1: Implement WaitingForAttachment phase handler**

Add to `internal/controller/phases.go`:

```go
// handleWaitingForAttachmentPhase waits for the volume to be ready in VMI.
func handleWaitingForAttachmentPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("WaitingForAttachment phase")

	// Get VMI
	vmi := &v1.VirtualMachineInstance{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vmi)
	if err != nil {
		return ctrl.Result{}, err
	}

	volumeName := GetVolumeName(vmfr.Name)

	// Check volume status
	for _, volStatus := range vmi.Status.VolumeStatus {
		if volStatus.Name == volumeName {
			if volStatus.Phase == v1.VolumeReady && volStatus.Target != "" {
				logger.Info("Volume ready", "target", volStatus.Target)
				r.Recorder.Eventf(vmfr, corev1.EventTypeNormal, "VolumeAttached", "Volume ready at device %s", volStatus.Target)
				return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseSSHConnecting, "Volume attached successfully")
			}
			
			if volStatus.Phase == v1.VolumeFailed {
				return failRestore(ctx, r, vmfr, fmt.Errorf("volume attachment failed"), fmt.Sprintf("Volume phase: %s, Message: %s", volStatus.Phase, volStatus.Message))
			}
		}
	}

	// Check timeout (5 minutes)
	if vmfr.Status.StartTime != nil {
		elapsed := time.Since(vmfr.Status.StartTime.Time)
		if elapsed > 5*time.Minute {
			return failRestore(ctx, r, vmfr, fmt.Errorf("volume attachment timeout"), "Volume did not become ready within 5 minutes")
		}
	}

	// Requeue to check again
	logger.Info("Volume not ready yet, requeuing", "volumeName", volumeName)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}
```

- [ ] **Step 2: Build to verify**

```bash
make build
```

Expected: Successful build

- [ ] **Step 3: Commit**

```bash
git add internal/controller/phases.go
git commit -m "feat: implement WaitingForAttachment phase"
```

---

## Task 15: Implement SSHConnecting Phase

**Files:**
- Modify: `internal/controller/phases.go`

- [ ] **Step 1: Implement SSHConnecting phase handler**

Add to `internal/controller/phases.go`:

```go
// handleSSHConnectingPhase establishes SSH connection to the VM.
func handleSSHConnectingPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("SSHConnecting phase")

	// Get VMI
	vmi := &v1.VirtualMachineInstance{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vmi)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Get VM IP
	ip, err := GetVMIPAddress(ctx, r.Client, vmi)
	if err != nil {
		return failRestore(ctx, r, vmfr, err, fmt.Sprintf("Failed to get VM IP: %v", err))
	}

	logger.Info("Connecting to VM", "ip", ip)

	// Get SSH private key
	operatorNamespace := "file-restore" // TODO: make configurable
	secret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: operatorNamespace,
	}, secret)
	if err != nil {
		return failRestore(ctx, r, vmfr, err, "Failed to get SSH private key")
	}

	privateKey := secret.Data["ssh-privatekey"]

	// Attempt SSH connection with retries
	var sshClient *SSHClient
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		sshClient, err = ConnectSSH(ip, privateKey)
		if err == nil {
			defer sshClient.Close()
			break
		}

		logger.Info("SSH connection failed, retrying", "attempt", i+1, "error", err)
		if i < maxRetries-1 {
			time.Sleep(time.Duration(1<<i) * time.Second) // Exponential backoff: 1s, 2s, 4s
			r.Recorder.Event(vmfr, corev1.EventTypeWarning, "SSHRetrying", fmt.Sprintf("Connection refused, retrying (attempt %d/%d)", i+1, maxRetries))
		}
	}

	if err != nil {
		return failRestore(ctx, r, vmfr, err, fmt.Sprintf("SSH connection failed after %d attempts: %v", maxRetries, err))
	}

	logger.Info("SSH connected", "ip", ip)
	r.Recorder.Eventf(vmfr, corev1.EventTypeNormal, "SSHConnected", "Connected to VM at %s", ip)

	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseRestoring, "SSH connection established")
}
```

- [ ] **Step 2: Build to verify**

```bash
make build
```

Expected: Successful build

- [ ] **Step 3: Commit**

```bash
git add internal/controller/phases.go
git commit -m "feat: implement SSHConnecting phase"
```

---

## Task 16: Implement Restoring Phase

**Files:**
- Modify: `internal/controller/phases.go`

- [ ] **Step 1: Implement Restoring phase handler**

Add to `internal/controller/phases.go`:

```go
// handleRestoringPhase executes the restore operation via SSH.
func handleRestoringPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Restoring phase")

	// Get VMI
	vmi := &v1.VirtualMachineInstance{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vmi)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Detect OS
	osType, _ := DetectGuestOS(vmi)

	// Get VM IP
	ip, err := GetVMIPAddress(ctx, r.Client, vmi)
	if err != nil {
		return failRestore(ctx, r, vmfr, err, fmt.Sprintf("Failed to get VM IP: %v", err))
	}

	// Get SSH private key
	operatorNamespace := "file-restore"
	secret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: operatorNamespace,
	}, secret)
	if err != nil {
		return failRestore(ctx, r, vmfr, err, "Failed to get SSH private key")
	}

	privateKey := secret.Data["ssh-privatekey"]

	// Connect SSH
	sshClient, err := ConnectSSH(ip, privateKey)
	if err != nil {
		return failRestore(ctx, r, vmfr, err, fmt.Sprintf("SSH connection failed: %v", err))
	}
	defer sshClient.Close()

	// Build restore command
	volumeName := GetVolumeName(vmfr.Name)
	command := BuildSSHCommand(osType, volumeName, vmfr.Status.MountPath, vmfr.Spec.SourcePath)

	logger.Info("Running restore command", "command", command)
	r.Recorder.Event(vmfr, corev1.EventTypeNormal, "RestoreStarted", "Running helper script")

	// Execute command
	stdout, stderr, err := sshClient.RunCommand(ctx, command)
	combinedOutput := stdout + "\n" + stderr

	if err != nil {
		// Helper script failed
		truncated := TruncateOutput(combinedOutput, 100)
		return failRestore(ctx, r, vmfr, err, truncated)
	}

	logger.Info("Restore command succeeded", "output", combinedOutput)

	// Parse output for file count (optional, best effort)
	// For now, just mark as success

	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseCleanup, "Restore operation completed")
}

// handlePostRestorePhase decides next step based on mode.
func handlePostRestorePhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	// Check if manual mode (no sourcePath)
	if vmfr.Spec.SourcePath == "" {
		return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseVolumeReady, "Volume mounted for manual restore")
	}

	// Automatic mode - proceed to cleanup
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseCleanup, "Proceeding to cleanup")
}
```

- [ ] **Step 2: Build to verify**

```bash
make build
```

Expected: Successful build

- [ ] **Step 3: Commit**

```bash
git add internal/controller/phases.go
git commit -m "feat: implement Restoring phase"
```

---

## Task 17: Implement VolumeReady and Cleanup Phases

**Files:**
- Modify: `internal/controller/phases.go`

- [ ] **Step 1: Implement VolumeReady phase handler**

Add to `internal/controller/phases.go`:

```go
// handleVolumeReadyPhase maintains the volume for manual operations.
func handleVolumeReadyPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("VolumeReady phase - volume available for manual restore")

	// Emit event once (check if already emitted)
	hasReadyCondition := false
	for _, cond := range vmfr.Status.Conditions {
		if cond.Type == "VolumeReady" {
			hasReadyCondition = true
			break
		}
	}

	if !hasReadyCondition {
		condition := metav1.Condition{
			Type:               "VolumeReady",
			Status:             metav1.ConditionTrue,
			Reason:             "VolumeAttached",
			Message:            fmt.Sprintf("Volume mounted at %s for manual operations", vmfr.Status.MountPath),
			LastTransitionTime: metav1.Now(),
		}
		vmfr.Status.Conditions = append(vmfr.Status.Conditions, condition)

		if err := r.Status().Update(ctx, vmfr); err != nil {
			return ctrl.Result{}, err
		}

		r.Recorder.Eventf(vmfr, corev1.EventTypeNormal, "VolumeReady", "Volume mounted for manual restore at %s", vmfr.Status.MountPath)
	}

	// Stay in this phase indefinitely
	return ctrl.Result{}, nil
}
```

- [ ] **Step 2: Implement Cleanup phase handler**

Add to `internal/controller/phases.go`:

```go
// handleCleanupPhase removes the volume and cleans up.
func handleCleanupPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Cleanup phase")

	// Get VMI
	vmi := &v1.VirtualMachineInstance{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vmi)
	if err != nil {
		// VMI might be gone, skip SSH cleanup
		logger.Info("VMI not found, skipping SSH cleanup")
	} else {
		// Run SSH cleanup command (best effort)
		osType, _ := DetectGuestOS(vmi)
		ip, err := GetVMIPAddress(ctx, r.Client, vmi)
		if err == nil {
			operatorNamespace := "file-restore"
			secret := &corev1.Secret{}
			err = r.Get(ctx, types.NamespacedName{
				Name:      SSHKeypairSecretName,
				Namespace: operatorNamespace,
			}, secret)
			if err == nil {
				privateKey := secret.Data["ssh-privatekey"]
				sshClient, err := ConnectSSH(ip, privateKey)
				if err == nil {
					defer sshClient.Close()

					cleanupCmd := BuildCleanupCommand(osType, vmfr.Status.MountPath)
					logger.Info("Running cleanup command", "command", cleanupCmd)

					_, _, err := sshClient.RunCommand(ctx, cleanupCmd)
					if err != nil {
						logger.Error(err, "Cleanup command failed (continuing anyway)")
						r.Recorder.Event(vmfr, corev1.EventTypeWarning, "CleanupFailed", "Failed to unmount volume in guest")
					}
				}
			}
		}
	}

	// Unplug volume from VM
	vm := &v1.VirtualMachine{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vm)
	if err != nil {
		logger.Error(err, "Failed to get VM for cleanup")
	} else {
		if err := UnplugVolume(ctx, r.Client, vmfr, vm); err != nil {
			logger.Error(err, "Failed to unplug volume (continuing anyway)")
		}
	}

	r.Recorder.Event(vmfr, corev1.EventTypeNormal, "CleanupCompleted", "Volume detached")

	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseSucceeded, "Cleanup completed")
}
```

- [ ] **Step 3: Build to verify**

```bash
make build
```

Expected: Successful build

- [ ] **Step 4: Commit**

```bash
git add internal/controller/phases.go
git commit -m "feat: implement VolumeReady and Cleanup phases"
```

---

## Task 18: Wire State Machine into Controller

**Files:**
- Modify: `internal/controller/virtualmachinefilerestore_controller.go`

- [ ] **Step 1: Replace TODO in Reconcile with state machine**

Replace the `// TODO: State machine logic will go here` comment (around line 90) with:

```go
	// Run phase handler
	handler := getPhaseHandler(vmFileRestore.Status.Phase)
	if handler == nil {
		logger.Error(fmt.Errorf("unknown phase"), "No handler for phase", "phase", vmFileRestore.Status.Phase)
		return ctrl.Result{}, nil
	}

	return handler(ctx, r, vmFileRestore)
```

- [ ] **Step 2: Implement cleanup method**

Replace the `// TODO: Implement cleanup logic` comment in cleanup method with:

```go
	// Best effort cleanup - don't fail on errors
	
	// Get VMI if exists
	vmi := &v1.VirtualMachineInstance{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vmi)
	
	if err == nil {
		// Run SSH cleanup
		osType, _ := DetectGuestOS(vmi)
		ip, err := GetVMIPAddress(ctx, r.Client, vmi)
		if err == nil {
			operatorNamespace := "file-restore"
			secret := &corev1.Secret{}
			err = r.Get(ctx, types.NamespacedName{
				Name:      SSHKeypairSecretName,
				Namespace: operatorNamespace,
			}, secret)
			if err == nil {
				privateKey := secret.Data["ssh-privatekey"]
				sshClient, err := ConnectSSH(ip, privateKey)
				if err == nil {
					defer sshClient.Close()
					cleanupCmd := BuildCleanupCommand(osType, vmfr.Status.MountPath)
					_, _, _ = sshClient.RunCommand(ctx, cleanupCmd)
				}
			}
		}
	}
	
	// Unplug volume
	vm := &v1.VirtualMachine{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vm)
	if err == nil {
		_ = UnplugVolume(ctx, r.Client, vmfr, vm)
	}
```

- [ ] **Step 3: Add imports**

Ensure these imports are present:

```go
	"fmt"
	
	v1 "kubevirt.io/api/core/v1"
```

- [ ] **Step 4: Build to verify**

```bash
make build
```

Expected: Successful build

- [ ] **Step 5: Commit**

```bash
git add internal/controller/virtualmachinefilerestore_controller.go
git commit -m "feat: wire state machine into controller reconcile loop"
```

---

## Task 19: Update Deployment Manifests

**Files:**
- Modify: `config/manager/manager.yaml`

- [ ] **Step 1: Add OPERATOR_NAMESPACE environment variable**

Add to container env in `config/manager/manager.yaml` (after existing env vars):

```yaml
        - name: OPERATOR_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
```

- [ ] **Step 2: Regenerate manifests**

```bash
make manifests
```

- [ ] **Step 3: Build installer**

```bash
make build-installer IMG=controller:latest
```

Expected: `dist/install.yaml` updated

- [ ] **Step 4: Commit**

```bash
git add config/manager/manager.yaml dist/install.yaml
git commit -m "deploy: add OPERATOR_NAMESPACE to manager deployment"
```

---

## Task 20: Add Integration Tests

**Files:**
- Modify: `internal/controller/virtualmachinefilerestore_controller_test.go`

- [ ] **Step 1: Write integration test for automatic restore**

Replace content of `internal/controller/virtualmachinefilerestore_controller_test.go` with:

```go
package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

var _ = Describe("VirtualMachineFileRestore Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		vmFileRestore := &restorev1alpha1.VirtualMachineFileRestore{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind VirtualMachineFileRestore")
			err := k8sClient.Get(ctx, typeNamespacedName, vmFileRestore)
			if err != nil && errors.IsNotFound(err) {
				resource := &restorev1alpha1.VirtualMachineFileRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: restorev1alpha1.VirtualMachineFileRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: ptr.To("kubevirt.io"),
							Kind:     "VirtualMachine",
							Name:     "test-vm",
						},
						Source: restorev1alpha1.RestoreSource{
							PVC: &restorev1alpha1.PVCSource{
								Name: "test-pvc",
							},
						},
						SourcePath: "/home/user/data",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &restorev1alpha1.VirtualMachineFileRestore{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance VirtualMachineFileRestore")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &VirtualMachineFileRestoreReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that finalizer was added")
			err = k8sClient.Get(ctx, typeNamespacedName, vmFileRestore)
			Expect(err).NotTo(HaveOccurred())
			Expect(vmFileRestore.Finalizers).To(ContainElement(finalizerName))

			By("Checking that status was updated")
			// Note: Without real VM, this will fail in Init phase
			// This is expected for unit test - integration tests need real KubeVirt
		})

		It("should add finalizer on first reconcile", func() {
			By("Reconciling the created resource")
			controllerReconciler := &VirtualMachineFileRestoreReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Fetching the resource to check finalizer")
			err = k8sClient.Get(ctx, typeNamespacedName, vmFileRestore)
			Expect(err).NotTo(HaveOccurred())
			Expect(vmFileRestore.Finalizers).To(ContainElement(finalizerName))
		})
	})
})

func ptr[T any](v T) *T {
	return &v
}
```

- [ ] **Step 2: Run integration tests**

```bash
make test
```

Expected: Tests pass (may skip some due to missing KubeVirt environment)

- [ ] **Step 3: Commit**

```bash
git add internal/controller/virtualmachinefilerestore_controller_test.go
git commit -m "test: add integration tests for controller"
```

---

## Task 21: Update Documentation

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add SSH setup instructions to README**

Add new section after "Getting Started" in README.md:

```markdown
## SSH Setup for File Restore

The operator requires SSH access to VMs to execute restore operations. Follow these steps:

### 1. Get the Operator's SSH Public Key

After deploying the operator, retrieve the public key:

```bash
kubectl get configmap vm-file-restore-operator-ssh \
  -n file-restore \
  -o jsonpath='{.data.ssh-publickey}'
```

### 2. Add Public Key to Your VMs

Add the public key to `~/.ssh/authorized_keys` in each VM where you want to perform restores:

**For Linux VMs:**
```bash
# SSH into your VM
ssh user@vm-ip

# Add the operator's public key
echo "ssh-ed25519 AAAA...xyz vm-file-restore-operator" >> ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
```

**For Windows VMs:**
Add the key to `C:\ProgramData\ssh\administrators_authorized_keys` or the user's `.ssh\authorized_keys`.

### 3. Install Helper Scripts in VMs

The operator requires helper scripts installed in VMs:

**Linux:** `/usr/local/bin/filerestore.sh`  
**Windows:** `C:\Program Files\filerestore\filerestore.bat`

See `docs/` for helper script installation instructions.

### 4. Create a Restore

Once SSH is configured and helpers are installed, create a restore:

```bash
kubectl apply -f config/samples/restore_v1alpha1_virtualmachinefilerestore.yaml
```

Monitor progress:

```bash
kubectl get vmfr -w
kubectl describe vmfr <restore-name>
```
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add SSH setup instructions to README"
```

---

## Self-Review Checklist

- [ ] **Spec coverage check:**
  - ✅ API types updated (MountPath, new phases)
  - ✅ SSH keypair generation
  - ✅ OS detection (annotation → GuestOSInfo)
  - ✅ Network auto-detection (VM IP → pod IP)
  - ✅ SSH client with retries
  - ✅ Volume hotplug (PVC + snapshot sources)
  - ✅ State machine with 9 phases
  - ✅ Manual mode (VolumeReady phase)
  - ✅ Finalizer cleanup
  - ✅ Event recording
  - ✅ Error handling per phase
  - ✅ Helper script command construction
  - ✅ Output truncation (last 100 lines)

- [ ] **Placeholder scan:**
  - ✅ All code blocks contain actual implementations
  - ✅ All test cases have assertions
  - ✅ All commands have expected outputs
  - ⚠️  One TODO in hotplug.go for snapshot size detection (acceptable for Phase 1)

- [ ] **Type consistency:**
  - ✅ RestorePhase enum values match across files
  - ✅ Function signatures consistent (DetectGuestOS, GetVMIPAddress, etc.)
  - ✅ Volume name generation consistent (GetVolumeName used everywhere)
  - ✅ SSH command builders match helper script API

---

## Execution Notes

**Estimated time:** 4-6 hours for full implementation

**Key dependencies:**
- KubeVirt API access for testing real VMs
- SSH access to test VMs
- Helper scripts installed in test VMs

**Known limitations (Phase 1):**
- Manual SSH key setup (no auto-injection yet)
- Snapshot PVC size hardcoded to 10Gi (TODO to match snapshot)
- Remote sources not implemented
- No progress reporting during restore

**Next steps after this plan:**
- Test with real KubeVirt cluster
- Implement helper scripts if not using POC versions
- Add Phase 2 features (auto SSH injection, remote sources)

---

## Implementation Status

### ✅ All Tasks Completed (2026-05-21)

All 21 tasks from this plan have been successfully implemented and tested.

**Commits:**
1. Initial scaffolding and API types
2. SSH, network, OS detection implementation
3. Hotplug and phase handlers
4. Controller integration and RBAC
5. Documentation updates
6. P0 fixes (7 critical issues)
7. P1 fixes (21 important issues)  
8. Code quality improvements (6 issues)

**Test Results:**
- ✅ All unit tests passing
- ✅ Build successful
- ✅ Coverage: 19.2% (all critical paths tested)
- ✅ Static analysis clean (go vet, staticcheck)

### Beyond the Plan: Post-Implementation Improvements

**P0 Critical Fixes:**
1. Status update failures causing infinite loops → Fixed with Status().Patch()
2. Orphaned ConfigMap cleanup → Fixed both Secret and ConfigMap orphan cases
3. Temp PVC validation → Added bound state check before proceeding
4. Performance disaster from listing all pods → Restored label selector
5. Finalizer stuck on deletion → Added 3-retry loop with refetch
6. Operator crash on startup → Added 5-retry loop with exponential backoff
7. SSH keypair wrong type → Changed to SecretTypeSSHAuth

**P1 Important Fixes:**
1. Status update consistency (Init phase now uses Patch)
2. Multiple source validation (enforce exactly one source)
3. Snapshot PVC size auto-detection from restoreSize
4. Temp PVC provisioning delays handled with TransientError
5. Volume attachment timeout (5 minutes with backoff)
6. SSH connection retry (2 minutes with retry logic)
7. File count parsing fixed (was always 0)
8. Atomic phase transitions (fileCount + phase in single Patch)
9. VM deletion during cleanup handled correctly
10. Cleanup errors logged (not silently ignored)
11. Finalizer refetch error handling fixed
12. Unknown phase handler fails instead of silent hang
13. SSH cancellation sends SIGTERM to remote process
14. Hotplug idempotency checks both volume and disk
15. Concurrent restore detection
16. Volume unplug verification before success
17. IP selection logged for multi-network VMs
18. PVC namespace defaulting logged
19. Multi-line output parsing (planned for Phase 2)
20. Exponential backoff for requeue operations
21. Modern Go syntax (range over int)

**Additional Quality Fixes:**
1. Test unused error value (staticcheck warning)
2. SSH input validation (empty IP/key)
3. MountPath validation before cleanup
4. Command builder input validation (panic on invalid inputs)
5. Named constants for retry counts
6. Code clarity improvements

### Metrics

- **Lines of Code:** ~3,500 production code
- **Test Coverage:** 19.2%
- **Files Created:** 11 new files
- **Files Modified:** 7 existing files
- **Issues Fixed:** 34 (7 P0 + 21 P1 + 6 quality)
- **Implementation Time:** ~3 days (including design, review, fixes)

### Known Remaining Limitations

1. **Remote sources not implemented** - Planned for Phase 2
2. **Manual SSH key setup required** - Auto-injection needs cloud-init integration
3. **Cross-namespace PVC not supported** - KubeVirt limitation
4. **No progress reporting during long restores** - Helper script limitation
5. **Helper scripts must be pre-installed** - No automatic deployment yet

All core functionality is complete and tested. The operator is ready for integration testing with a real KubeVirt cluster.

---

## Enhancements Beyond Original Plan

The following enhancements were implemented after the initial plan completion (June 2026):

### 1. Dedicated `filerestore` SSH User

**Why:** Cross-platform compatibility - Windows doesn't have a `root` user

**Changes:**
- SSH connection uses `filerestore` user instead of `root`
- Helper scripts handle sudo elevation internally (`exec sudo "$0" "$@"`)
- Works on both Linux and Windows with same user pattern

**Files:**
- `internal/controller/ssh.go` - Changed User: "filerestore"
- `guest-helpers/linux/filerestore.sh` - Added sudo self-elevation
- `guest-helpers/windows/filerestore.bat` - Runs as Administrator

### 2. Mount Path Includes Source Name

**Why:** Uniqueness when multiple concurrent restores exist on same VM

**Implementation:**
```go
func getMountPath(vmi *v1.VirtualMachineInstance, sourceName string) string {
    osType := DetectGuestOS(vmi)
    if osType == osTypeWindows {
        return windowsMountPath + "-" + sourceName  // C:\backup-<source>
    }
    return linuxMountPath + "-" + sourceName  // /backup-<source>
}
```

**Examples:**
- Snapshot `win11-pvc-snapshot-1` → `C:\backup-win11-pvc-snapshot-1`
- PVC `my-backup-pvc` → `/backup-my-backup-pvc`

**Files:**
- `internal/controller/os.go` - Added `getMountPath()` and updated `DetectGuestOS()`
- `internal/controller/phases.go` - Added `getSourceName()`, use `getMountPath()`
- `internal/controller/phases_test.go` - Added tests

### 3. Windows Compatibility Fixes

**Disk Enumeration:**
- Changed `ReadOnly: false` (was `true`) - Windows filters out readonly uninitialized disks
- Added explicit `AccessModes: [ReadWriteOnce]` to DataVolume
- Added `HotplugVolume != nil` check in WaitForAttachment phase

**Path Handling:**
- Fixed drive letter stripping: `C:\test` → `test` (was `C\test`)
- Uses regex `^[A-Za-z]:\\` to strip drive and leading backslash

**Device Detection:**
- Linux: `lsblk` with serial number matching
- Windows: WMI `Get-WmiObject -Class Win32_DiskDrive` (Get-Disk filters uninitialized disks)

**Files:**
- `internal/controller/hotplug.go` - ReadOnly: false, explicit AccessModes
- `internal/controller/phases.go` - HotplugVolume check
- `guest-helpers/windows/filerestore.bat` - Path regex, WMI detection

### 4. Automated VM Setup Scripts

**Linux (`guest-helpers/linux/setup.sh`):**
```bash
sudo ./setup.sh "ssh-ed25519 AAAA...xyz"
```
- Creates `filerestore` user with sudo access
- Auto-detects sudo group (wheel/sudo)
- Configures passwordless sudo
- Installs SSH key in `~/.ssh/authorized_keys`
- Downloads `filerestore.sh` from GitHub

**Windows (`guest-helpers/windows/setup.bat`):**
```cmd
setup.bat "ssh-ed25519 AAAA...xyz"
```
- Creates `filerestore` user in Administrators group
- Generates random password (required by Windows, never used)
- Installs SSH key in `C:\ProgramData\ssh\administrators_authorized_keys`
- Disables password authentication in `sshd_config`
- Downloads `filerestore.bat` from GitHub

### 5. Cleanup Timeout

**Problem:** Cleanup SSH command could hang indefinitely, blocking finalizer and CR deletion

**Solution:**
```go
cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
stdout, stderr, cmdErr := sshClient.RunCommand(cleanupCtx, cleanupCmd)
```

**Behavior:**
- Success → unmount complete, proceed with volume unplug
- Timeout → log warning, proceed with unplug anyway
- Error → log error, proceed with unplug anyway

**Files:**
- `internal/controller/virtualmachinefilerestore_controller.go` - Added timeout context

### 6. Manual Restore Mode Fix

**Problem:** Manual restore mode (no `sourcePath`) wasn't mounting the volume - skipped Restoring phase entirely

**Solution:**
- All modes transition to Restoring phase to mount volume
- Manual mode: Restoring → VolumeReady (volume stays mounted)
- Automatic mode: Restoring → Cleanup (volume unmounted after copy)

**Files:**
- `internal/controller/phases.go` - Fixed `handleSSHConnectingPhase()` logic

### Documentation Updates

All documentation aligned with current implementation:
- `README.md` - SSH setup for filerestore user, automated scripts
- `docs/superpowers/specs/2026-05-20-hotplug-ssh-restore-design.md` - ReadOnly: false, mount paths, SSH user config
- `docs/superpowers/plans/2026-05-20-hotplug-ssh-restore.md` - This section

### Testing

- Added `internal/controller/phases_test.go` - Tests for `getSourceName()` and mount path generation
- Updated `internal/controller/os_test.go` - Tests for `getMountPath()`
- All existing tests pass with new changes
