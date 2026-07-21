/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v6/apis/volumesnapshot/v1"
	snapshotclientset "github.com/kubernetes-csi/external-snapshotter/client/v6/clientset/versioned"
	ginkgo "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"
	cdiv1beta1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	filerestorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
	"kubevirt.io/vm-file-restore-operator/test/utils"
)

const (
	namespace    = "file-restore"
	vmName       = "fedora-file-restore-test"
	bootDiskName = "fedora-boot-dv"
	bootDiskSize = "10Gi"
)

type ExtraDisk struct {
	Name string
	Size string
}

type TestEnv struct {
	K8sClient      *kubernetes.Clientset
	VirtClient     kubecli.KubevirtClient
	SnapshotClient snapshotclientset.Interface
	CRClient       client.Client
	Namespace      string
	PrivateKeyPath string
}

func setupTestVM(nsPrefix string, extraDisks ...ExtraDisk) *TestEnv {
	env := &TestEnv{}

	ginkgo.By("initializing Kubernetes clients")
	var err error
	env.K8sClient, env.VirtClient, env.SnapshotClient, env.CRClient, err = initClients()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to initialize clients")

	ginkgo.By("creating unique test namespace")
	env.Namespace = fmt.Sprintf("%s-%d", nsPrefix, ginkgo.GinkgoRandomSeed())
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: env.Namespace},
	}
	_, err = env.K8sClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create test namespace")
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "Created test namespace: %s\n", env.Namespace)

	ginkgo.DeferCleanup(func() {
		ginkgo.By("cleaning up test resources")
		if env.PrivateKeyPath != "" {
			_ = os.RemoveAll(filepath.Dir(env.PrivateKeyPath))
		}
		_ = env.K8sClient.CoreV1().Namespaces().Delete(context.Background(), env.Namespace, metav1.DeleteOptions{})
	})

	ginkgo.By("generating temporary SSH keypair")
	tmpDir, err := os.MkdirTemp("", nsPrefix+"-ssh-")
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create temp directory")
	env.PrivateKeyPath = tmpDir + "/id_ed25519"

	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", env.PrivateKeyPath, "-N", "", "-C", nsPrefix)
	keygenOutput, err := cmd.CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to generate SSH keypair: %s", string(keygenOutput))

	pubKeyBytes, err := os.ReadFile(env.PrivateKeyPath + ".pub")
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to read public key")
	pubKey := strings.TrimSpace(string(pubKeyBytes))

	ginkgo.By("fetching operator's SSH public key from ConfigMap")
	cm, err := env.K8sClient.CoreV1().ConfigMaps(namespace).Get(
		context.Background(), "vm-file-restore-operator-ssh", metav1.GetOptions{},
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get operator SSH ConfigMap")
	operatorPubKey := cm.Data["ssh-publickey"]
	gomega.Expect(operatorPubKey).NotTo(gomega.BeEmpty(), "Operator SSH public key is empty")

	ginkgo.By("creating test VirtualMachine")
	err = createTestVM(env.VirtClient, env.Namespace, vmName, pubKey, bootDiskName, bootDiskSize, extraDisks...)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create VM")

	ginkgo.By("waiting for VM to reach Running state")
	gomega.Eventually(func(g gomega.Gomega) {
		vmi, err := env.VirtClient.VirtualMachineInstance(env.Namespace).Get(
			context.Background(), vmName, metav1.GetOptions{},
		)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get VMI")
		g.Expect(vmi.Status.Phase).To(gomega.Equal(kubevirtv1.Running), "VMI not running")
	}, 5*time.Minute, 10*time.Second).Should(gomega.Succeed())

	ginkgo.By("waiting for SSH connectivity")
	gomega.Eventually(func(g gomega.Gomega) {
		_, err := runSSHCommand(vmName, env.Namespace, "echo ready", env.PrivateKeyPath)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "SSH not ready")
	}, 5*time.Minute, 15*time.Second).Should(gomega.Succeed())

	ginkgo.By("installing guest helper")
	_, testFile, _, _ := goruntime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(testFile), "../..")
	setupScriptPath := filepath.Join(repoRoot, "guest-helpers/linux/setup.sh")
	setupScript, err := os.ReadFile(setupScriptPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to read setup script")
	setupCmd := fmt.Sprintf("cat <<'SETUP_EOF' | bash -s -- '%s'\n%s\nSETUP_EOF", operatorPubKey, string(setupScript))
	gomega.Eventually(func(g gomega.Gomega) {
		_, err := runSSHCommand(vmName, env.Namespace, setupCmd, env.PrivateKeyPath)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "Guest helper installation failed")
	}, 2*time.Minute, 10*time.Second).Should(gomega.Succeed())

	ginkgo.By("overwriting filerestore.sh with local version")
	localScriptPath := filepath.Join(repoRoot, "guest-helpers/linux/filerestore.sh")
	localScript, err := os.ReadFile(localScriptPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to read local filerestore.sh")
	installCmd := fmt.Sprintf(
		"cat <<'SCRIPT_EOF' | sudo tee /usr/local/bin/filerestore.sh"+
			" > /dev/null && sudo chmod +x /usr/local/bin/filerestore.sh\n%s\nSCRIPT_EOF",
		string(localScript),
	)
	_, err = runSSHCommand(vmName, env.Namespace, installCmd, env.PrivateKeyPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to install local filerestore.sh")

	return env
}

// initClients creates and returns Kubernetes, KubeVirt, snapshot, and controller-runtime clients
func initClients() (
	*kubernetes.Clientset, kubecli.KubevirtClient, snapshotclientset.Interface, client.Client, error,
) {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		return nil, nil, nil, nil, fmt.Errorf("KUBECONFIG environment variable not set")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to build config: %w", err)
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	virtClient, err := kubecli.GetKubevirtClientFromRESTConfig(config)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create kubevirt client: %w", err)
	}

	snapshotClient, err := snapshotclientset.NewForConfig(config)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create snapshot client: %w", err)
	}

	// Create controller-runtime scheme with our API types
	scheme := runtime.NewScheme()
	if err := filerestorev1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to add filerestore scheme: %w", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to add corev1 scheme: %w", err)
	}
	if err := snapshotv1.AddToScheme(scheme); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to add snapshot scheme: %w", err)
	}
	if err := kubevirtv1.AddToScheme(scheme); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to add kubevirt scheme: %w", err)
	}

	// Create controller-runtime client for typed access to our CRs
	crClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	return k8sClient, virtClient, snapshotClient, crClient, nil
}

// runSSHCommand executes a command in the VM via virtctl ssh with default 5-minute timeout
func runSSHCommand(vmiName, namespace, command, identityFile string) (string, error) {
	return runSSHCommandWithTimeout(vmiName, namespace, command, identityFile, 5*time.Minute)
}

// runSSHCommandWithTimeout executes a command in the VM via virtctl ssh with configurable timeout
func runSSHCommandWithTimeout(vmiName, namespace, command, identityFile string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "virtctl", "ssh",
		"-n", namespace,
		"-i", identityFile,
		"--local-ssh-opts=-o LogLevel=ERROR",
		"--local-ssh-opts=-o StrictHostKeyChecking=no",
		"--local-ssh-opts=-o UserKnownHostsFile=/dev/null",
		fmt.Sprintf("root@vmi/%s", vmiName),
		"--command", command,
	)
	cmd.Stdin = nil
	output, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("SSH command timed out after %v: %w", timeout, err)
	}
	return strings.TrimSpace(string(output)), err
}

// shellEscape escapes a string for safe use in shell commands by wrapping in single quotes
// and escaping any embedded single quotes
func shellEscape(s string) string {
	// Replace each single quote with '\'' (end quote, escaped quote, start quote)
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// getFileSizeFromVM queries file size via SSH and parses the result
func getFileSizeFromVM(vmiName, namespace, filePath, identityFile string) (int64, error) {
	output, err := runSSHCommand(vmiName, namespace, fmt.Sprintf("stat -c %%s %s", shellEscape(filePath)), identityFile)
	if err != nil {
		return 0, fmt.Errorf("failed to stat file: %w", err)
	}
	size, err := strconv.ParseInt(output, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse file size %q: %w", output, err)
	}
	return size, nil
}

// createTestVM creates a VirtualMachine with a Fedora boot disk (DataVolume) and cloud-init for SSH key injection
func createTestVM(
	virtClient kubecli.KubevirtClient, ns, name, sshPubKey, bootDisk, diskSize string,
	extraDisks ...ExtraDisk,
) error {
	cloudInitUserData := fmt.Sprintf(`#cloud-config
users:
  - name: root
    ssh_authorized_keys:
      - %s
`, sshPubKey)

	disks := []kubevirtv1.Disk{
		{
			Name: bootDisk,
			DiskDevice: kubevirtv1.DiskDevice{
				Disk: &kubevirtv1.DiskTarget{Bus: "virtio"},
			},
		},
	}
	volumes := []kubevirtv1.Volume{
		{
			Name: bootDisk,
			VolumeSource: kubevirtv1.VolumeSource{
				DataVolume: &kubevirtv1.DataVolumeSource{Name: bootDisk},
			},
		},
	}
	dvTemplates := []kubevirtv1.DataVolumeTemplateSpec{
		{
			ObjectMeta: metav1.ObjectMeta{Name: bootDisk},
			Spec: cdiv1beta1.DataVolumeSpec{
				Source: &cdiv1beta1.DataVolumeSource{
					Registry: &cdiv1beta1.DataVolumeSourceRegistry{
						URL: func() *string { s := "docker://quay.io/containerdisks/fedora:44"; return &s }(),
					},
				},
				Storage: &cdiv1beta1.StorageSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse(diskSize),
						},
					},
				},
			},
		},
	}

	for _, d := range extraDisks {
		disks = append(disks, kubevirtv1.Disk{
			Name: d.Name,
			DiskDevice: kubevirtv1.DiskDevice{
				Disk: &kubevirtv1.DiskTarget{Bus: "virtio"},
			},
		})
		volumes = append(volumes, kubevirtv1.Volume{
			Name: d.Name,
			VolumeSource: kubevirtv1.VolumeSource{
				DataVolume: &kubevirtv1.DataVolumeSource{Name: d.Name},
			},
		})
		dvTemplates = append(dvTemplates, kubevirtv1.DataVolumeTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Name: d.Name},
			Spec: cdiv1beta1.DataVolumeSpec{
				Source: &cdiv1beta1.DataVolumeSource{
					Blank: &cdiv1beta1.DataVolumeBlankImage{},
				},
				Storage: &cdiv1beta1.StorageSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse(d.Size),
						},
					},
				},
			},
		})
	}

	disks = append(disks, kubevirtv1.Disk{
		Name: "cloudinitdisk",
		DiskDevice: kubevirtv1.DiskDevice{
			Disk: &kubevirtv1.DiskTarget{Bus: "virtio"},
		},
	})
	volumes = append(volumes, kubevirtv1.Volume{
		Name: "cloudinitdisk",
		VolumeSource: kubevirtv1.VolumeSource{
			CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{
				UserData: cloudInitUserData,
			},
		},
	})

	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: func() *kubevirtv1.VirtualMachineRunStrategy { s := kubevirtv1.RunStrategyAlways; return &s }(),
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						CPU:    &kubevirtv1.CPU{Cores: 1},
						Memory: &kubevirtv1.Memory{Guest: resource.NewQuantity(2*1024*1024*1024, resource.BinarySI)},
						Devices: kubevirtv1.Devices{
							Disks: disks,
							Interfaces: []kubevirtv1.Interface{
								{
									Name:                   "default",
									InterfaceBindingMethod: kubevirtv1.InterfaceBindingMethod{Masquerade: &kubevirtv1.InterfaceMasquerade{}},
								},
							},
						},
					},
					Networks: []kubevirtv1.Network{
						{Name: "default", NetworkSource: kubevirtv1.NetworkSource{Pod: &kubevirtv1.PodNetwork{}}},
					},
					Subdomain: "headless",
					Volumes:   volumes,
				},
			},
			DataVolumeTemplates: dvTemplates,
		},
	}

	_, err := virtClient.VirtualMachine(ns).Create(context.Background(), vm, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create VirtualMachine %s/%s: %w", ns, name, err)
	}
	return nil
}

// createVolumeSnapshot creates a VolumeSnapshot for the VM's disk PVC
func createVolumeSnapshot(
	snapshotClient snapshotclientset.Interface,
	k8sClient *kubernetes.Clientset,
	namespace, pvcName, snapName string,
) error {
	// Get the PVC to find its StorageClass and provisioner
	pvc, err := k8sClient.CoreV1().PersistentVolumeClaims(namespace).Get(
		context.Background(),
		pvcName,
		metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to get PVC: %w", err)
	}

	var provisioner string
	if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
		sc, err := k8sClient.StorageV1().StorageClasses().Get(
			context.Background(),
			*pvc.Spec.StorageClassName,
			metav1.GetOptions{},
		)
		if err != nil {
			return fmt.Errorf("failed to get StorageClass %q: %w", *pvc.Spec.StorageClassName, err)
		}
		provisioner = sc.Provisioner
	}

	// Find a VolumeSnapshotClass with matching driver (provisioner)
	var snapshotClassName *string
	snapshotClasses, err := snapshotClient.SnapshotV1().VolumeSnapshotClasses().List(
		context.Background(),
		metav1.ListOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to list VolumeSnapshotClasses: %w", err)
	}
	if len(snapshotClasses.Items) == 0 {
		return fmt.Errorf("no VolumeSnapshotClasses found in cluster")
	}

	// First, try to find one with matching driver
	if provisioner != "" {
		for i := range snapshotClasses.Items {
			if snapshotClasses.Items[i].Driver == provisioner {
				snapshotClassName = &snapshotClasses.Items[i].Name
				break
			}
		}
	}
	// If no match, look for default class
	if snapshotClassName == nil {
		for i := range snapshotClasses.Items {
			sc := &snapshotClasses.Items[i]
			if sc.Annotations != nil && sc.Annotations["snapshot.storage.kubernetes.io/is-default-class"] == "true" {
				snapshotClassName = &sc.Name
				break
			}
		}
	}
	// If still no match, use the first one
	if snapshotClassName == nil {
		snapshotClassName = &snapshotClasses.Items[0].Name
	}

	snapshot := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      snapName,
			Namespace: namespace,
		},
		Spec: snapshotv1.VolumeSnapshotSpec{
			VolumeSnapshotClassName: snapshotClassName,
			Source: snapshotv1.VolumeSnapshotSource{
				PersistentVolumeClaimName: &pvcName,
			},
		},
	}

	_, err = snapshotClient.SnapshotV1().VolumeSnapshots(namespace).Create(
		context.Background(),
		snapshot,
		metav1.CreateOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to create VolumeSnapshot %s/%s for PVC %s: %w", namespace, snapName, pvcName, err)
	}
	return nil
}

// createFileRestoreCR creates a VirtualMachineFileRestore custom resource
func createFileRestoreCR(crClient client.Client, ns, restoreName, targetVM, snapshot, sourcePath string) error {
	restore := &filerestorev1alpha1.VirtualMachineFileRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreName,
			Namespace: ns,
		},
		Spec: filerestorev1alpha1.VirtualMachineFileRestoreSpec{
			Target: corev1.TypedLocalObjectReference{
				APIGroup: func() *string { s := "kubevirt.io"; return &s }(),
				Kind:     "VirtualMachine",
				Name:     targetVM,
			},
			Source: filerestorev1alpha1.RestoreSource{
				Snapshot: &filerestorev1alpha1.VolumeSnapshotSource{
					Name: snapshot,
				},
			},
			SourcePath: sourcePath,
		},
	}

	return crClient.Create(context.Background(), restore)
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken(ns, saName string) (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", saName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", fmt.Errorf("failed to write token request to %s: %w", tokenRequestFile, err)
	}
	defer func() { _ = os.Remove(tokenRequestFile) }()

	var out string
	verifyTokenCreation := func(g gomega.Gomega) {
		// Execute kubectl command to create the token with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			ns,
			saName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		out = token.Status.Token
	}
	gomega.Eventually(verifyTokenCreation).Should(gomega.Succeed())

	return out, nil
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput(ns string) string {
	ginkgo.By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", ns)
	metricsOutput, err := utils.Run(cmd)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to retrieve logs from curl pod")
	gomega.Expect(metricsOutput).To(gomega.ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
