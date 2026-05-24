//nolint:goconst // Test constants are acceptable
package controller

import (
	"context"
	"testing"

	"golang.org/x/crypto/ssh"
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

	// Verify Secret type is SSHAuth
	if secret.Type != corev1.SecretTypeSSHAuth {
		t.Errorf("Secret type is %s, expected %s", secret.Type, corev1.SecretTypeSSHAuth)
	}

	if _, ok := secret.Data[corev1.SSHAuthPrivateKey]; !ok {
		t.Errorf("%s not found in Secret", corev1.SSHAuthPrivateKey)
	}

	// Validate private key is actually parseable
	privKeyBytes := secret.Data[corev1.SSHAuthPrivateKey]
	if _, err := ssh.ParsePrivateKey(privKeyBytes); err != nil {
		t.Errorf("Private key is not a valid SSH key: %v", err)
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

	// Validate public key is actually parseable
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubKey)); err != nil {
		t.Errorf("Public key is not a valid SSH authorized key: %v", err)
	}
}

func TestEnsureSSHKeypair_SkipsIfBothExist(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SSHKeypairSecretName,
			Namespace: "test-ns",
		},
		Type: corev1.SecretTypeSSHAuth,
		Data: map[string][]byte{
			corev1.SSHAuthPrivateKey: []byte("existing-key"),
		},
	}

	existingConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SSHKeypairSecretName,
			Namespace: "test-ns",
		},
		Data: map[string]string{
			"ssh-publickey": "existing-pubkey",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existingSecret, existingConfigMap).
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

	if string(secret.Data[corev1.SSHAuthPrivateKey]) != "existing-key" {
		t.Error("Secret was modified when both resources existed")
	}

	// Verify ConfigMap unchanged
	cm := &corev1.ConfigMap{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: "test-ns",
	}, cm)
	if err != nil {
		t.Fatal(err)
	}

	if cm.Data["ssh-publickey"] != "existing-pubkey" {
		t.Error("ConfigMap was modified when both resources existed")
	}
}

func TestEnsureSSHKeypair_CleansUpOrphanedSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Create only a Secret without ConfigMap (orphaned state)
	orphanedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SSHKeypairSecretName,
			Namespace: "test-ns",
		},
		Type: corev1.SecretTypeSSHAuth,
		Data: map[string][]byte{
			corev1.SSHAuthPrivateKey: []byte("orphaned-key"),
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(orphanedSecret).
		Build()

	err := EnsureSSHKeypair(context.Background(), client, "test-ns")
	if err != nil {
		t.Fatalf("EnsureSSHKeypair failed: %v", err)
	}

	// Verify Secret was regenerated (different key)
	secret := &corev1.Secret{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: "test-ns",
	}, secret)
	if err != nil {
		t.Fatal(err)
	}

	if string(secret.Data[corev1.SSHAuthPrivateKey]) == "orphaned-key" {
		t.Error("Orphaned Secret was not regenerated")
	}

	// Verify ConfigMap was created
	cm := &corev1.ConfigMap{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      SSHKeypairSecretName,
		Namespace: "test-ns",
	}, cm)
	if err != nil {
		t.Fatalf("ConfigMap not created after orphaned Secret cleanup: %v", err)
	}
}
