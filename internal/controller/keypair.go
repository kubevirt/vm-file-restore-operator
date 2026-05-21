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
	secretErr := c.Get(ctx, types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: namespace,
	}, secret)

	// Check if ConfigMap already exists
	configMap := &corev1.ConfigMap{}
	cmErr := c.Get(ctx, types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: namespace,
	}, configMap)

	// If both exist, skip generation
	if secretErr == nil && cmErr == nil {
		return nil
	}

	// If Secret exists but ConfigMap doesn't, cleanup orphaned Secret
	if secretErr == nil && errors.IsNotFound(cmErr) {
		if err := c.Delete(ctx, secret); err != nil {
			return fmt.Errorf("failed to cleanup orphaned Secret: %w", err)
		}
	}

	// If there are any non-NotFound errors, fail
	if secretErr != nil && !errors.IsNotFound(secretErr) {
		return fmt.Errorf("failed to check for existing Secret: %w", secretErr)
	}
	if cmErr != nil && !errors.IsNotFound(cmErr) {
		return fmt.Errorf("failed to check for existing ConfigMap: %w", cmErr)
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
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SSHKeypairSecretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeSSHAuth,
		Data: map[string][]byte{
			corev1.SSHAuthPrivateKey: privKeyPEM,
		},
	}

	if err := c.Create(ctx, newSecret); err != nil {
		return fmt.Errorf("failed to create Secret: %w", err)
	}

	// Create ConfigMap for public key
	newConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SSHKeypairSecretName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"ssh-publickey": pubKeyStr,
		},
	}

	if err := c.Create(ctx, newConfigMap); err != nil {
		return fmt.Errorf("failed to create ConfigMap: %w", err)
	}

	return nil
}
