package controller

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	linuxhelpers "kubevirt.io/vm-file-restore-operator/guest-helpers/linux"
	windowshelpers "kubevirt.io/vm-file-restore-operator/guest-helpers/windows"
)

const (
	// OperatorResourceName is the shared name of the Secret and ConfigMap that store the SSH keypair,
	// public key, and guest helper tarballs.
	OperatorResourceName = "vm-file-restore-operator-ssh"

	cmKeyPublicKey      = "ssh-publickey"
	cmKeyLinuxHelpers   = "linux-helpers.tar"
	cmKeyWindowsHelpers = "windows-helpers.tar"

	keypairSyncInterval = 10 * time.Second
)

// KeypairEnsurer is a manager.Runnable that periodically checks that the operator's
// SSH keypair Secret and ConfigMap exist, recreating them if they were deleted.
// It uses the provided client directly (non-cached) so it sees live API state.
type KeypairEnsurer struct {
	Client    client.Client
	Namespace string
}

func (k *KeypairEnsurer) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("keypair-ensurer")
	ticker := time.NewTicker(keypairSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := EnsureOperatorResources(ctx, k.Client, k.Namespace); err != nil {
				logger.Error(err, "Failed to ensure SSH keypair resources")
			}
		}
	}
}

var _ manager.Runnable = &KeypairEnsurer{}

// EnsureOperatorResources creates the operator's SSH keypair and guest helper
// tarballs if they don't exist. Private key is stored in a Secret; public key
// and helper tars (linux-helpers.tar, windows-helpers.tar) are stored in a
// ConfigMap. On upgrade, tarballs are kept in sync with the embedded scripts.
func EnsureOperatorResources(ctx context.Context, c client.Client, namespace string) error {
	secret := &corev1.Secret{}
	secretErr := c.Get(ctx, types.NamespacedName{Name: OperatorResourceName, Namespace: namespace}, secret)

	configMap := &corev1.ConfigMap{}
	cmErr := c.Get(ctx, types.NamespacedName{Name: OperatorResourceName, Namespace: namespace}, configMap)

	// Both exist: reconcile the public key first, then sync helper tarballs.
	// Reconciliation is re-attempted on every poll so a transient update failure
	// (conflict, API error) does not leave a permanently mismatched keypair.
	if secretErr == nil && cmErr == nil {
		if err := reconcilePublicKey(ctx, c, secret, configMap); err != nil {
			return err
		}
		return ensureHelperTars(ctx, c, configMap)
	}

	// Surface transient API errors before any orphan logic.
	if secretErr != nil && !errors.IsNotFound(secretErr) {
		return fmt.Errorf("failed to check for existing Secret: %w", secretErr)
	}
	if cmErr != nil && !errors.IsNotFound(cmErr) {
		return fmt.Errorf("failed to check for existing ConfigMap: %w", cmErr)
	}

	// Secret exists but ConfigMap is missing: recover by creating the ConfigMap from
	// the existing keypair. Deleting the Secret would revoke SSH access for every VM
	// already configured with the corresponding public key.
	if secretErr == nil && errors.IsNotFound(cmErr) {
		return createConfigMapFromSecret(ctx, c, namespace, secret)
	}

	// ConfigMap exists but Secret is missing: the public key is useless without the
	// private key. Delete the orphaned ConfigMap and regenerate both.
	if errors.IsNotFound(secretErr) && cmErr == nil {
		if err := c.Delete(ctx, configMap); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to cleanup orphaned ConfigMap: %w", err)
		}
	}

	return createKeypairAndConfigMap(ctx, c, namespace)
}

// createConfigMapFromSecret recreates the ConfigMap using the public key derived
// from the existing private key in the Secret. This preserves the keypair so VMs
// already configured with the public key remain accessible.
func createConfigMapFromSecret(ctx context.Context, c client.Client, namespace string, secret *corev1.Secret) error {
	privKeyPEM, ok := secret.Data[corev1.SSHAuthPrivateKey]
	if !ok || len(privKeyPEM) == 0 {
		return fmt.Errorf("existing Secret is missing %s; cannot recover ConfigMap without the private key", corev1.SSHAuthPrivateKey)
	}
	signer, err := ssh.ParsePrivateKey(privKeyPEM)
	if err != nil {
		return fmt.Errorf("failed to parse existing private key: %w", err)
	}
	return createConfigMap(ctx, c, namespace, string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

// createKeypairAndConfigMap generates a new ED25519 keypair, stores the private
// key in a Secret, and creates the ConfigMap with the public key and helper tarballs.
func createKeypairAndConfigMap(ctx context.Context, c client.Client, namespace string) error {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate keypair: %w", err)
	}

	privKeyBytes, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("failed to create SSH public key: %w", err)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: OperatorResourceName, Namespace: namespace},
		Type:       corev1.SecretTypeSSHAuth,
		Data:       map[string][]byte{corev1.SSHAuthPrivateKey: pem.EncodeToMemory(privKeyBytes)},
	}
	if err := c.Create(ctx, newSecret); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create Secret: %w", err)
		}
		existing := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Name: OperatorResourceName, Namespace: namespace}, existing); err != nil {
			return fmt.Errorf("failed to get existing Secret: %w", err)
		}
		return createConfigMapFromSecret(ctx, c, namespace, existing)
	}

	return createConfigMap(ctx, c, namespace, string(ssh.MarshalAuthorizedKey(sshPublicKey)))
}

// createConfigMap creates the operator ConfigMap with the given public key and
// the embedded guest helper tarballs in binaryData.
func createConfigMap(ctx context.Context, c client.Client, namespace, pubKeyStr string) error {
	linuxTar, err := buildTar(linuxhelpers.Scripts, linuxhelpers.SetupScriptName, linuxhelpers.FileRestoreScriptName)
	if err != nil {
		return fmt.Errorf("failed to build linux helpers tar: %w", err)
	}
	windowsTar, err := buildTar(windowshelpers.Scripts, windowshelpers.SetupScriptName, windowshelpers.FileRestoreScriptName)
	if err != nil {
		return fmt.Errorf("failed to build windows helpers tar: %w", err)
	}

	newConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: OperatorResourceName, Namespace: namespace},
		Data:       map[string]string{cmKeyPublicKey: pubKeyStr},
		BinaryData: map[string][]byte{
			cmKeyLinuxHelpers:   linuxTar,
			cmKeyWindowsHelpers: windowsTar,
		},
	}
	if err := c.Create(ctx, newConfigMap); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create ConfigMap: %w", err)
		}
		// Re-fetch both resources to get authoritative state: pubKeyStr may be stale if
		// the Secret was rotated between the caller's initial read and this point.
		liveSecret := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Name: OperatorResourceName, Namespace: namespace}, liveSecret); err != nil {
			return fmt.Errorf("failed to get Secret for ConfigMap reconciliation: %w", err)
		}
		existing := &corev1.ConfigMap{}
		if err := c.Get(ctx, types.NamespacedName{Name: OperatorResourceName, Namespace: namespace}, existing); err != nil {
			return fmt.Errorf("failed to get existing ConfigMap: %w", err)
		}
		if err := reconcilePublicKey(ctx, c, liveSecret, existing); err != nil {
			return err
		}
	}
	return nil
}

// reconcilePublicKey checks whether the ConfigMap's public key matches the
// private key stored in the Secret and updates the ConfigMap if they differ.
func reconcilePublicKey(ctx context.Context, c client.Client, secret *corev1.Secret, cm *corev1.ConfigMap) error {
	privKeyPEM := secret.Data[corev1.SSHAuthPrivateKey]
	if len(privKeyPEM) == 0 {
		return fmt.Errorf("secret is missing %s", corev1.SSHAuthPrivateKey)
	}
	signer, err := ssh.ParsePrivateKey(privKeyPEM)
	if err != nil {
		return fmt.Errorf("failed to parse private key: %w", err)
	}
	wantPubKey := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if cm.Data[cmKeyPublicKey] == wantPubKey {
		return nil
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[cmKeyPublicKey] = wantPubKey
	if err := c.Update(ctx, cm); err != nil {
		return fmt.Errorf("failed to reconcile ConfigMap public key: %w", err)
	}
	return nil
}

// ensureHelperTars keeps the ConfigMap's helper tarballs in sync with the scripts
// embedded in the running operator binary. It updates only when the content differs,
// so operator upgrades automatically propagate updated scripts.
func ensureHelperTars(ctx context.Context, c client.Client, cm *corev1.ConfigMap) error {
	linuxTar, err := buildTar(linuxhelpers.Scripts, linuxhelpers.SetupScriptName, linuxhelpers.FileRestoreScriptName)
	if err != nil {
		return fmt.Errorf("failed to build linux helpers tar: %w", err)
	}
	windowsTar, err := buildTar(windowshelpers.Scripts, windowshelpers.SetupScriptName, windowshelpers.FileRestoreScriptName)
	if err != nil {
		return fmt.Errorf("failed to build windows helpers tar: %w", err)
	}

	if cm.BinaryData == nil {
		cm.BinaryData = make(map[string][]byte)
	}
	if bytes.Equal(cm.BinaryData[cmKeyLinuxHelpers], linuxTar) &&
		bytes.Equal(cm.BinaryData[cmKeyWindowsHelpers], windowsTar) {
		return nil
	}

	cm.BinaryData[cmKeyLinuxHelpers] = linuxTar
	cm.BinaryData[cmKeyWindowsHelpers] = windowsTar

	if err := c.Update(ctx, cm); err != nil {
		if errors.IsConflict(err) {
			// Concurrent operator pod (HA rolling upgrade) won the race; refetch and retry once.
			if getErr := c.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}, cm); getErr != nil {
				return fmt.Errorf("failed to re-fetch ConfigMap after conflict: %w", getErr)
			}
			if cm.BinaryData == nil {
				cm.BinaryData = make(map[string][]byte)
			}
			// Re-check after refetch: the winning pod may have already applied current tars.
			if bytes.Equal(cm.BinaryData[cmKeyLinuxHelpers], linuxTar) &&
				bytes.Equal(cm.BinaryData[cmKeyWindowsHelpers], windowsTar) {
				return nil
			}
			cm.BinaryData[cmKeyLinuxHelpers] = linuxTar
			cm.BinaryData[cmKeyWindowsHelpers] = windowsTar
			if retryErr := c.Update(ctx, cm); retryErr != nil {
				return fmt.Errorf("failed to update ConfigMap with helper tars (after conflict retry): %w", retryErr)
			}
			return nil
		}
		return fmt.Errorf("failed to update ConfigMap with helper tars: %w", err)
	}
	return nil
}

// buildTar creates an uncompressed tar archive from the named files in the given FS.
// Shell scripts (.sh) are stored with mode 0755 so they are directly executable after extraction.
func buildTar(scripts fs.ReadFileFS, names ...string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range names {
		content, err := scripts.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		mode := int64(0644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0755
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: mode,
			Size: int64(len(content)),
		}); err != nil {
			return nil, fmt.Errorf("write tar header for %s: %w", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			return nil, fmt.Errorf("write tar content for %s: %w", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	return buf.Bytes(), nil
}
