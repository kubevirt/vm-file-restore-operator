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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v6/apis/volumesnapshot/v1"
	snapshotclientset "github.com/kubernetes-csi/external-snapshotter/client/v6/clientset/versioned"
	ginkgo "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	"kubevirt.io/vm-file-restore-operator/internal/controller"
	"kubevirt.io/vm-file-restore-operator/test/utils"
)

const (
	vmName           = "fedora-file-restore-test"
	bootDiskName     = "fedora-boot-dv"
	bootDiskSize     = "10Gi"
	kubevirtAPIGroup = "kubevirt.io"
)

func kubevirtAPIGroupPtr() *string {
	s := kubevirtAPIGroup
	return &s
}

type ExtraDisk struct {
	Name string
	Size string
}

// setupTestVMOptions controls optional steps in setupTestVM.
type setupTestVMOptions struct {
	skipGuestHelper bool
}

type TestEnv struct {
	K8sClient      *kubernetes.Clientset
	VirtClient     kubecli.KubevirtClient
	SnapshotClient snapshotclientset.Interface
	CRClient       client.WithWatch
	Namespace      string
	PrivateKeyPath string
}

func setupTestVM(nsPrefix string, extraDisks ...ExtraDisk) *TestEnv {
	return setupTestVMWithOptions(nsPrefix, setupTestVMOptions{}, extraDisks...)
}

// setupTestVMWithoutGuestHelper creates a running VM with root SSH only.
// The filerestore user / helper are not installed, so operator guest SSH fails.
func setupTestVMWithoutGuestHelper(nsPrefix string, extraDisks ...ExtraDisk) *TestEnv {
	return setupTestVMWithOptions(nsPrefix, setupTestVMOptions{skipGuestHelper: true}, extraDisks...)
}

// newTestEnv creates clients and a unique namespace with cleanup registered.
func newTestEnv(nsPrefix string) *TestEnv {
	env := &TestEnv{}

	ginkgo.By("initializing Kubernetes clients")
	var err error
	env.K8sClient, env.VirtClient, env.SnapshotClient, env.CRClient, err = initClients()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to initialize clients")

	ginkgo.By("creating unique test namespace")
	env.Namespace = fmt.Sprintf("%s-%d", nsPrefix, time.Now().UnixNano())
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: env.Namespace},
	}
	_, err = env.K8sClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create test namespace")
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "Created test namespace: %s\n", env.Namespace)

	ginkgo.DeferCleanup(func() {
		ginkgo.By("cleaning up test namespace")
		_ = env.K8sClient.CoreV1().Namespaces().Delete(context.Background(), env.Namespace, metav1.DeleteOptions{})
	})

	return env
}

// setupTestEnv creates clients and a unique namespace without a VM.
// Useful for admission/reconcile validation that fails before needing a target VMI.
func setupTestEnv(nsPrefix string) *TestEnv {
	return newTestEnv(nsPrefix)
}

func setupTestVMWithOptions(nsPrefix string, opts setupTestVMOptions, extraDisks ...ExtraDisk) *TestEnv {
	env := newTestEnv(nsPrefix)

	ginkgo.DeferCleanup(func() {
		ginkgo.By("cleaning up test SSH key material")
		if env.PrivateKeyPath != "" {
			_ = os.RemoveAll(filepath.Dir(env.PrivateKeyPath))
		}
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

	ginkgo.By("fetching operator SSH public key and guest helpers from ConfigMap")
	cm, err := env.K8sClient.CoreV1().ConfigMaps(operatorNamespace()).Get(
		context.Background(), operatorSSHConfigMapName(), metav1.GetOptions{},
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get operator SSH ConfigMap")
	operatorPubKey := strings.TrimSpace(cm.Data["ssh-publickey"])
	gomega.Expect(operatorPubKey).NotTo(gomega.BeEmpty(), "Operator SSH public key is empty")
	linuxHelperTar := cm.BinaryData["linux-helpers.tar"]
	gomega.Expect(linuxHelperTar).NotTo(gomega.BeEmpty(), "linux-helpers.tar not found in operator ConfigMap")

	ginkgo.By("creating test VirtualMachine")
	err = createTestVM(env.VirtClient, env.Namespace, vmName, pubKey, bootDiskName, bootDiskSize, extraDisks...)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create VM")

	vmRunningTimeout := 10 * time.Minute
	sshReadyTimeout := 10 * time.Minute
	dvNames := make([]string, 0, 1+len(extraDisks))
	dvNames = append(dvNames, bootDiskName)
	for _, d := range extraDisks {
		dvNames = append(dvNames, d.Name)
	}
	ginkgo.By("waiting for VM DataVolumes to be ready")
	waitForDataVolumesReady(env.CRClient, env.Namespace, vmRunningTimeout, dvNames...)

	ginkgo.By("waiting for VM to reach Running state")
	gomega.Eventually(func(g gomega.Gomega) {
		vmi, err := env.VirtClient.VirtualMachineInstance(env.Namespace).Get(
			context.Background(), vmName, metav1.GetOptions{},
		)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get VMI")
		g.Expect(vmi.Status.Phase).To(gomega.Equal(kubevirtv1.Running),
			"VMI not running (%s)", vmiStatusDetail(vmi))
	}, vmRunningTimeout, 10*time.Second).Should(gomega.Succeed())

	ginkgo.By("waiting for SSH connectivity")
	gomega.Eventually(func(g gomega.Gomega) {
		_, err := runSSHCommand(vmName, env.Namespace, "echo ready", env.PrivateKeyPath)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "SSH not ready")
	}, sshReadyTimeout, 15*time.Second).Should(gomega.Succeed())

	if !opts.skipGuestHelper {
		ginkgo.By("installing guest helper with operator's SSH key")
		gomega.Eventually(func(g gomega.Gomega) {
			err := installGuestHelper(vmName, env.Namespace, operatorPubKey, linuxHelperTar, env.PrivateKeyPath)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "Guest helper installation failed")
		}, 2*time.Minute, 10*time.Second).Should(gomega.Succeed())
	}

	return env
}

// vmiStatusDetail formats VMI phase and notable conditions for failure messages.
func vmiStatusDetail(vmi *kubevirtv1.VirtualMachineInstance) string {
	if vmi == nil {
		return "VMI is nil"
	}
	parts := []string{fmt.Sprintf("phase=%s", vmi.Status.Phase)}
	for _, cond := range vmi.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			parts = append(parts, fmt.Sprintf("%s=%s", cond.Type, cond.Message))
		}
	}
	return strings.Join(parts, "; ")
}

// initClients creates and returns Kubernetes, KubeVirt, snapshot, and controller-runtime clients
func initClients() (
	*kubernetes.Clientset, kubecli.KubevirtClient, snapshotclientset.Interface, client.WithWatch, error,
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
	if err := cdiv1beta1.AddToScheme(scheme); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to add cdi scheme: %w", err)
	}

	// Create controller-runtime client for typed access to our CRs
	crClient, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
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
			Name:   d.Name,
			Serial: d.Name,
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

// waitForDataVolumesReady waits until each named DataVolume reaches Succeeded.
func waitForDataVolumesReady(crClient client.Client, namespace string, timeout time.Duration, names ...string) {
	for _, name := range names {
		dvName := name
		gomega.Eventually(func(g gomega.Gomega) {
			current := &cdiv1beta1.DataVolume{}
			g.Expect(crClient.Get(context.Background(), client.ObjectKey{
				Namespace: namespace, Name: dvName,
			}, current)).To(gomega.Succeed(), "Failed to get DataVolume %s", dvName)
			g.Expect(current.Status.Phase).To(gomega.Equal(cdiv1beta1.Succeeded),
				"DataVolume %s not Succeeded (phase: %s)", dvName, current.Status.Phase)
		}, timeout, 10*time.Second).Should(gomega.Succeed())
	}
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

// createFileRestoreCR creates a VirtualMachineFileRestore custom resource from a VolumeSnapshot.
func createFileRestoreCR(crClient client.Client, ns, restoreName, snapshot, sourcePath string) error {
	restore := &filerestorev1alpha1.VirtualMachineFileRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreName,
			Namespace: ns,
		},
		Spec: filerestorev1alpha1.VirtualMachineFileRestoreSpec{
			Target: corev1.TypedLocalObjectReference{
				APIGroup: kubevirtAPIGroupPtr(),
				Kind:     "VirtualMachine",
				Name:     vmName,
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

// createFileRestoreCRFromPVC creates a VirtualMachineFileRestore CR with a PVC source.
func createFileRestoreCRFromPVC(crClient client.Client, ns, restoreName, targetVM, pvcName, sourcePath string) error {
	restore := &filerestorev1alpha1.VirtualMachineFileRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreName,
			Namespace: ns,
		},
		Spec: filerestorev1alpha1.VirtualMachineFileRestoreSpec{
			Target: corev1.TypedLocalObjectReference{
				APIGroup: kubevirtAPIGroupPtr(),
				Kind:     "VirtualMachine",
				Name:     targetVM,
			},
			Source: filerestorev1alpha1.RestoreSource{
				PVC: &filerestorev1alpha1.PVCSource{
					Name: pvcName,
				},
			},
			SourcePath: sourcePath,
		},
	}

	return crClient.Create(context.Background(), restore)
}

// waitForVolumeSnapshotReady waits until the named VolumeSnapshot is ReadyToUse.
func waitForVolumeSnapshotReady(
	snapshotClient snapshotclientset.Interface, namespace, snapName string,
) {
	gomega.Eventually(func(g gomega.Gomega) {
		snapshot, err := snapshotClient.SnapshotV1().VolumeSnapshots(namespace).Get(
			context.Background(), snapName, metav1.GetOptions{},
		)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get VolumeSnapshot")
		g.Expect(snapshot.Status).NotTo(gomega.BeNil(), "VolumeSnapshot has no status")
		g.Expect(snapshot.Status.ReadyToUse).NotTo(gomega.BeNil(), "VolumeSnapshot ReadyToUse is nil")
		g.Expect(*snapshot.Status.ReadyToUse).To(gomega.BeTrue(), "VolumeSnapshot not ready")
	}, 3*time.Minute, 10*time.Second).Should(gomega.Succeed())
}

// createPVCFromSnapshot creates a CDI DataVolume from a VolumeSnapshot and waits until
// the clone finishes. Bound alone is not enough: with CDI snapshot cloning the PVC can
// bind while the DataVolume is still importing, so we wait for DataVolume Succeeded.
func createPVCFromSnapshot(
	crClient client.Client, k8sClient *kubernetes.Clientset, namespace, snapName, pvcName string,
) {
	dv := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
		},
		Spec: cdiv1beta1.DataVolumeSpec{
			Source: &cdiv1beta1.DataVolumeSource{
				Snapshot: &cdiv1beta1.DataVolumeSourceSnapshot{
					Namespace: namespace,
					Name:      snapName,
				},
			},
			Storage: &cdiv1beta1.StorageSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
			},
		},
	}
	gomega.Expect(crClient.Create(context.Background(), dv)).To(gomega.Succeed(),
		"failed to create DataVolume from snapshot")

	gomega.Eventually(func(g gomega.Gomega) {
		current := &cdiv1beta1.DataVolume{}
		g.Expect(crClient.Get(context.Background(), client.ObjectKey{
			Namespace: namespace, Name: pvcName,
		}, current)).To(gomega.Succeed(), "Failed to get DataVolume from snapshot")
		g.Expect(current.Status.Phase).To(gomega.Equal(cdiv1beta1.Succeeded),
			"DataVolume not Succeeded (phase: %s)", current.Status.Phase)

		pvc, err := k8sClient.CoreV1().PersistentVolumeClaims(namespace).Get(
			context.Background(), pvcName, metav1.GetOptions{},
		)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get PVC from snapshot")
		g.Expect(pvc.Status.Phase).To(gomega.Equal(corev1.ClaimBound), "PVC not Bound")
	}, 5*time.Minute, 10*time.Second).Should(gomega.Succeed())
}

// getFileRestore returns the current VirtualMachineFileRestore CR.
func getFileRestore(crClient client.Client, ns, name string) *filerestorev1alpha1.VirtualMachineFileRestore {
	restore := &filerestorev1alpha1.VirtualMachineFileRestore{}
	err := crClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, restore)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get restore CR %s/%s", ns, name)
	return restore
}

// waitForRestorePhase waits until the restore CR reaches the expected phase.
func waitForRestorePhase(
	crClient client.Client, ns, name string, phase filerestorev1alpha1.RestorePhase,
) *filerestorev1alpha1.VirtualMachineFileRestore {
	var restore *filerestorev1alpha1.VirtualMachineFileRestore
	gomega.Eventually(func(g gomega.Gomega) {
		restore = &filerestorev1alpha1.VirtualMachineFileRestore{}
		err := crClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, restore)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get restore CR")
		switch {
		case phase != filerestorev1alpha1.RestorePhaseFailed &&
			restore.Status.Phase == filerestorev1alpha1.RestorePhaseFailed:
			gomega.StopTrying(fmt.Sprintf("restore reached Failed before %s: %s",
				phase, restore.Status.ErrorMessage)).Now()
		case phase != filerestorev1alpha1.RestorePhaseSucceeded &&
			restore.Status.Phase == filerestorev1alpha1.RestorePhaseSucceeded:
			gomega.StopTrying(fmt.Sprintf("restore reached Succeeded before %s", phase)).Now()
		}
		g.Expect(restore.Status.Phase).To(gomega.Equal(phase),
			fmt.Sprintf("Restore phase is %s, error: %s", restore.Status.Phase, restore.Status.ErrorMessage))
	}, 5*time.Minute, 10*time.Second).Should(gomega.Succeed())
	return restore
}

// waitForRestoreFailed waits until the restore CR reaches Failed with non-empty failure details.
func waitForRestoreFailed(
	crClient client.Client, ns, name string, timeout time.Duration,
) *filerestorev1alpha1.VirtualMachineFileRestore {
	var restore *filerestorev1alpha1.VirtualMachineFileRestore
	gomega.Eventually(func(g gomega.Gomega) {
		restore = &filerestorev1alpha1.VirtualMachineFileRestore{}
		err := crClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, restore)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get restore CR")
		if restore.Status.Phase == filerestorev1alpha1.RestorePhaseSucceeded {
			gomega.StopTrying("restore reached Succeeded instead of Failed").Now()
		}
		g.Expect(restore.Status.Phase).To(gomega.Equal(filerestorev1alpha1.RestorePhaseFailed),
			fmt.Sprintf("Restore phase is %s", restore.Status.Phase))
		g.Expect(restoreFailureText(restore)).NotTo(gomega.BeEmpty(), "Expected non-empty failure details")
	}, timeout, 10*time.Second).Should(gomega.Succeed())
	return restore
}

// restoreVolumeName returns the hotplug volume name used by the operator for a restore CR.
func restoreVolumeName(restoreCRName string) string {
	return controller.GetVolumeName(restoreCRName)
}

// vmiHasRestoreVolume reports whether the VMI still has the restore hotplug volume attached.
func vmiHasRestoreVolume(virtClient kubecli.KubevirtClient, namespace, vmiName, restoreCRName string) (bool, error) {
	vmi, err := virtClient.VirtualMachineInstance(namespace).Get(context.Background(), vmiName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	want := restoreVolumeName(restoreCRName)
	for _, vol := range vmi.Status.VolumeStatus {
		if vol.Name == want {
			return true, nil
		}
	}
	for _, vol := range vmi.Spec.Volumes {
		if vol.Name == want {
			return true, nil
		}
	}
	return false, nil
}

// assertRestoreVolumeDetached asserts the restore hotplug volume is gone from the VMI.
func assertRestoreVolumeDetached(virtClient kubecli.KubevirtClient, namespace, vmiName, restoreCRName string) {
	gomega.Eventually(func(g gomega.Gomega) {
		present, err := vmiHasRestoreVolume(virtClient, namespace, vmiName, restoreCRName)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(present).To(gomega.BeFalse(), "restore volume still attached to VMI")
	}, 2*time.Minute, 5*time.Second).Should(gomega.Succeed())
}

// assertNoManagedRestoreDataVolume asserts no operator-managed restore DataVolume remains for the CR.
func assertNoManagedRestoreDataVolume(crClient client.Client, namespace, restoreCRName string) {
	gomega.Eventually(func(g gomega.Gomega) {
		dv := &cdiv1beta1.DataVolume{}
		err := crClient.Get(context.Background(),
			client.ObjectKey{Namespace: namespace, Name: restoreVolumeName(restoreCRName)}, dv)
		g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(),
			"expected restore DataVolume to be deleted, got: %v", err)
	}, 2*time.Minute, 5*time.Second).Should(gomega.Succeed())
}

// assertSuccessfulRestoreCleanup checks temporary resources are removed after Succeeded (TS-009).
func assertSuccessfulRestoreCleanup(
	virtClient kubecli.KubevirtClient,
	crClient client.Client,
	namespace, vmiName, restoreCRName string,
	snapshotSource bool,
) {
	ginkgo.By("verifying hotplugged restore volume is detached")
	assertRestoreVolumeDetached(virtClient, namespace, vmiName, restoreCRName)

	if snapshotSource {
		ginkgo.By("verifying temporary restore DataVolume was cleaned up")
		assertNoManagedRestoreDataVolume(crClient, namespace, restoreCRName)
	}

	ginkgo.By("verifying restore CR remains Succeeded for audit")
	restore := getFileRestore(crClient, namespace, restoreCRName)
	gomega.Expect(restore.Status.Phase).To(gomega.Equal(filerestorev1alpha1.RestorePhaseSucceeded))
}

// getFileModeFromVM returns the octal mode bits for a file in the guest.
func getFileModeFromVM(vmiName, namespace, filePath, identityFile string) (string, error) {
	return runSSHCommand(vmiName, namespace, fmt.Sprintf("stat -c %%a %s", shellEscape(filePath)), identityFile)
}

// getFileOwnerGroupFromVM returns "user:group" ownership for a file in the guest.
func getFileOwnerGroupFromVM(vmiName, namespace, filePath, identityFile string) (string, error) {
	return runSSHCommand(vmiName, namespace, fmt.Sprintf("stat -c %%U:%%G %s", shellEscape(filePath)), identityFile)
}

// startConnectivityProbe runs SSH echo checks every interval until stop is closed.
// Read failures with failures.Load() after closing stop and waiting on done.
func startConnectivityProbe(
	vmiName, namespace, identityFile string, interval time.Duration,
) (stop chan struct{}, failures *atomic.Int32, done chan struct{}) {
	stop = make(chan struct{})
	done = make(chan struct{})
	failures = &atomic.Int32{}

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if _, err := runSSHCommandWithTimeout(vmiName, namespace, "echo ok", identityFile, 15*time.Second); err != nil {
					failures.Add(1)
					_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "connectivity probe failure: %v\n", err)
				}
			}
		}
	}()
	return stop, failures, done
}

// createFileRestoreOperatorCR creates the operator configuration CR via the API
// (no dependency on config/samples YAML on disk — required for standalone QE binaries).
func createFileRestoreOperatorCR(crClient client.Client, namespace, name string) error {
	fro := &filerestorev1alpha1.FileRestoreOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: filerestorev1alpha1.FileRestoreOperatorSpec{
			ImagePullPolicy: corev1.PullIfNotPresent,
		},
	}
	if err := crClient.Create(context.Background(), fro); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create FileRestoreOperator %s/%s: %w", namespace, name, err)
	}
	return nil
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

// deleteSnapshotIfExists deletes a VolumeSnapshot and waits until it is fully removed.
func deleteSnapshotIfExists(env *TestEnv, name string) {
	err := env.SnapshotClient.SnapshotV1().VolumeSnapshots(env.Namespace).Delete(
		context.Background(), name, metav1.DeleteOptions{},
	)
	if apierrors.IsNotFound(err) {
		return
	}
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to delete VolumeSnapshot %s/%s", env.Namespace, name)
	gomega.Eventually(func(g gomega.Gomega) {
		_, err := env.SnapshotClient.SnapshotV1().VolumeSnapshots(env.Namespace).Get(
			context.Background(), name, metav1.GetOptions{},
		)
		g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue())
	}, 2*time.Minute, 5*time.Second).Should(gomega.Succeed())
}

// deleteDVIfExists deletes a DataVolume and waits until it is fully removed.
func deleteDVIfExists(env *TestEnv, name string) {
	err := env.CRClient.Delete(context.Background(), &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: env.Namespace},
	})
	if apierrors.IsNotFound(err) {
		return
	}
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to delete DataVolume %s/%s", env.Namespace, name)
	gomega.Eventually(func(g gomega.Gomega) {
		err := env.CRClient.Get(context.Background(), client.ObjectKey{Namespace: env.Namespace, Name: name},
			&cdiv1beta1.DataVolume{})
		g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue())
	}, 2*time.Minute, 5*time.Second).Should(gomega.Succeed())
}

// deleteFileRestoreIfExists deletes a VirtualMachineFileRestore CR and waits for the operator
// finalizer to complete and the hotplugged volume to be detached from the shared VM.
func deleteFileRestoreIfExists(env *TestEnv, name string) {
	err := env.CRClient.Delete(context.Background(), &filerestorev1alpha1.VirtualMachineFileRestore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: env.Namespace},
	})
	if apierrors.IsNotFound(err) {
		assertRestoreVolumeDetached(env.VirtClient, env.Namespace, vmName, name)
		return
	}
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to delete VirtualMachineFileRestore %s/%s", env.Namespace,
		name)
	// Wait for the finalizer to run and the CR to be fully removed.
	gomega.Eventually(func(g gomega.Gomega) {
		current := &filerestorev1alpha1.VirtualMachineFileRestore{}
		err := env.CRClient.Get(context.Background(), client.ObjectKey{Namespace: env.Namespace, Name: name}, current)
		g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "VirtualMachineFileRestore %s/%s still exists", env.Namespace,
			name)
	}, 2*time.Minute, 5*time.Second).Should(gomega.Succeed())
	// Ensure the hotplugged volume is detached before the next test runs on the shared VM.
	assertRestoreVolumeDetached(env.VirtClient, env.Namespace, vmName, name)
}

const dummySSHPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG1vY2tlZGtleWZvcnRlc3Rzbm90b3BlcmF0b3I=" +
	" vmfr-e2e-dummy-key"

func ensureRestoreTestUser(env *TestEnv) {
	_, err := runSSHCommand(vmName, env.Namespace,
		fmt.Sprintf("id %s &>/dev/null || useradd -m -s /bin/bash %s", testUser, testUser), env.PrivateKeyPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to ensure restore test user on guest")
}

func waitForGuestFile(env *TestEnv, filePath string) {
	gomega.Eventually(func(g gomega.Gomega) {
		_, err := runSSHCommand(vmName, env.Namespace,
			fmt.Sprintf("test -f %s", shellEscape(filePath)), env.PrivateKeyPath)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "file %s not visible on guest yet", filePath)
	}, 30*time.Second, 2*time.Second).Should(gomega.Succeed())
}

func snapshotBootDisk(env *TestEnv, snapName string) {
	err := createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, snapName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create boot disk VolumeSnapshot")
	waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, snapName)
}

func assertErrorMessageContains(restore *filerestorev1alpha1.VirtualMachineFileRestore, substrings ...string) {
	msg := restoreFailureText(restore)
	gomega.Expect(msg).NotTo(gomega.BeEmpty(), "expected non-empty restore failure details")
	for _, want := range substrings {
		gomega.Expect(msg).To(gomega.ContainSubstring(strings.ToLower(want)),
			"restore failure details should contain %q (errorMessage=%q)", want, restore.Status.ErrorMessage)
	}
}

func restoreFailureText(restore *filerestorev1alpha1.VirtualMachineFileRestore) string {
	var parts []string
	if restore.Status.ErrorMessage != "" {
		parts = append(parts, strings.ToLower(restore.Status.ErrorMessage))
	}
	for _, condition := range restore.Status.Conditions {
		if condition.Message != "" {
			parts = append(parts, strings.ToLower(condition.Message))
		}
	}
	return strings.Join(parts, " ")
}

func getOperatorSSHResources(k8sClient *kubernetes.Clientset) (string, []byte) {
	configMap, err := k8sClient.CoreV1().ConfigMaps(operatorNamespace()).Get(
		context.Background(), operatorSSHConfigMapName(), metav1.GetOptions{},
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to get operator SSH ConfigMap")
	publicKey := strings.TrimSpace(configMap.Data["ssh-publickey"])
	gomega.Expect(publicKey).NotTo(gomega.BeEmpty(), "Operator SSH public key is empty")
	linuxTar := configMap.BinaryData["linux-helpers.tar"]
	gomega.Expect(linuxTar).NotTo(gomega.BeEmpty(), "linux-helpers.tar not found in operator ConfigMap")
	return publicKey, linuxTar
}

func installGuestHelperWithoutOperatorKey(env *TestEnv) {
	_, linuxTar := getOperatorSSHResources(env.K8sClient)
	gomega.Eventually(func(g gomega.Gomega) {
		err := installGuestHelper(vmName, env.Namespace, dummySSHPublicKey, linuxTar, env.PrivateKeyPath)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "Guest helper installation failed")
	}, 2*time.Minute, 10*time.Second).Should(gomega.Succeed())
}

func removeFilerestoreHelperBinary(env *TestEnv) {
	_, err := runSSHCommand(vmName, env.Namespace, "rm -f /usr/local/bin/filerestore.sh", env.PrivateKeyPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to remove filerestore.sh")
}

func stopVM(virtClient kubecli.KubevirtClient, namespace, name string) {
	halted := kubevirtv1.RunStrategyHalted
	patch := fmt.Appendf(nil, `{"spec":{"runStrategy":"%s"}}`, halted)
	_, err := virtClient.VirtualMachine(namespace).Patch(
		context.Background(), name, "application/merge-patch+json", patch, metav1.PatchOptions{},
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to halt VM")
	gomega.Eventually(func(g gomega.Gomega) {
		_, err := virtClient.VirtualMachineInstance(namespace).Get(context.Background(), name, metav1.GetOptions{})
		g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "VMI still exists after halt")
	}, 5*time.Minute, 10*time.Second).Should(gomega.Succeed())
}

func interruptGuestRestoreWhileRunning(
	crClient client.WithWatch, namespace, restoreName string,
	timeout time.Duration,
	action func() error,
) <-chan error {
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	stream, err := crClient.Watch(ctx, &filerestorev1alpha1.VirtualMachineFileRestoreList{},
		client.InNamespace(namespace), client.MatchingFields{"metadata.name": restoreName})
	if err != nil {
		cancel()
		result <- fmt.Errorf("watch restore %s: %w", restoreName, err)
		close(result)
		return result
	}

	go func() {
		defer close(result)
		defer cancel()
		defer stream.Stop()
		for {
			select {
			case <-ctx.Done():
				result <- fmt.Errorf("restore did not reach Restoring within %s", timeout)
				return
			case event, ok := <-stream.ResultChan():
				if !ok {
					result <- fmt.Errorf("restore watch closed before %s reached Restoring", restoreName)
					return
				}
				restore, ok := event.Object.(*filerestorev1alpha1.VirtualMachineFileRestore)
				if !ok || restore.Name != restoreName {
					continue
				}
				switch restore.Status.Phase {
				case filerestorev1alpha1.RestorePhaseFailed, filerestorev1alpha1.RestorePhaseSucceeded:
					result <- fmt.Errorf("restore reached terminal phase %s before interruption", restore.Status.Phase)
					return
				case filerestorev1alpha1.RestorePhaseRestoring:
					result <- action()
					return
				}
			}
		}
	}()
	return result
}

// pauseGuestRsync waits for the actual file copy, then stops its processes so
// a hot-unplug cannot race with the transfer finishing. It signals only rsync
// PIDs, leaving the SSH session running so it can return the PIDs to the caller.
func pauseGuestRsync(vmName, namespace, identityFile string) ([]int, error) {
	const pauseCommand = `for i in $(seq 1 1200); do
    if pgrep -x rsync >/dev/null 2>&1; then
        if pkill -STOP -x rsync 2>/dev/null; then
            pids=$(pgrep -x rsync | tr '\n' ' ')
            if [ -n "$pids" ]; then
                echo "$pids"
                exit 0
            fi
        fi
    fi
    sleep 0.05
done
echo "rsync did not start within 60 seconds" >&2
exit 1`
	processes, err := runSSHCommandWithTimeout(vmName, namespace, pauseCommand, identityFile, 90*time.Second)
	if err != nil {
		return nil, fmt.Errorf("pause guest rsync: %w (output: %s)", err, processes)
	}
	pidStrings := strings.Fields(processes)
	if len(pidStrings) == 0 {
		return nil, fmt.Errorf("pause guest rsync returned no process IDs")
	}
	processIDs := make([]int, 0, len(pidStrings))
	for _, pidString := range pidStrings {
		pid, err := strconv.Atoi(pidString)
		if err != nil {
			return nil, fmt.Errorf("parse guest rsync process ID %q: %w", pidString, err)
		}
		processIDs = append(processIDs, pid)
	}
	return processIDs, nil
}

func resumeGuestRsync(vmName, namespace, identityFile string, processIDs []int) error {
	if len(processIDs) == 0 {
		return fmt.Errorf("cannot resume guest rsync: no process IDs")
	}
	pidStrings := make([]string, len(processIDs))
	for i, pid := range processIDs {
		pidStrings[i] = strconv.Itoa(pid)
	}
	_, err := runSSHCommand(vmName, namespace,
		fmt.Sprintf("kill -CONT %s", strings.Join(pidStrings, " ")), identityFile)
	if err != nil {
		return fmt.Errorf("resume guest rsync processes %s: %w", strings.Join(pidStrings, ","), err)
	}
	return nil
}

func interruptGuestDiskDuringRestore(
	virtClient kubecli.KubevirtClient, namespace, vmName, volumeName, identityFile string,
) (resultErr error) {
	processIDs, err := pauseGuestRsync(vmName, namespace, identityFile)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, resumeGuestRsync(vmName, namespace, identityFile, processIDs))
	}()

	device, err := findDataDiskDevice(vmName, namespace, volumeName, identityFile)
	if err != nil {
		return fmt.Errorf("find guest disk %s: %w", volumeName, err)
	}
	if err := removeVolumeFromVM(virtClient, namespace, vmName, volumeName); err != nil {
		return err
	}
	if err := waitForVMIVolumeDetached(virtClient, namespace, vmName, volumeName, 2*time.Minute); err != nil {
		return err
	}
	if err := waitForGuestBlockDeviceDetached(
		vmName, namespace, device, identityFile, 2*time.Minute,
	); err != nil {
		return err
	}
	return nil
}

func createBlankBackupPVC(k8sClient *kubernetes.Clientset, namespace, pvcName, size string) {
	storageClassName := defaultStorageClassName(k8sClient)
	blockMode := corev1.PersistentVolumeBlock
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			VolumeMode:  &blockMode,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
			StorageClassName: storageClassName,
		},
	}
	_, err := k8sClient.CoreV1().PersistentVolumeClaims(namespace).Create(
		context.Background(), pvc, metav1.CreateOptions{},
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create blank backup PVC")
}

func defaultStorageClassName(k8sClient *kubernetes.Clientset) *string {
	storageClasses, err := k8sClient.StorageV1().StorageClasses().List(context.Background(), metav1.ListOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for _, storageClass := range storageClasses.Items {
		if storageClass.Annotations != nil &&
			storageClass.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			return &storageClass.Name
		}
	}
	if len(storageClasses.Items) > 0 {
		return &storageClasses.Items[0].Name
	}
	return nil
}

func removeVolumeFromVM(
	virtClient kubecli.KubevirtClient, namespace, vmName, volumeName string,
) error {
	if err := virtClient.VirtualMachine(namespace).RemoveVolume(
		context.Background(), vmName, &kubevirtv1.RemoveVolumeOptions{Name: volumeName},
	); err != nil {
		return fmt.Errorf("remove volume %s from VM %s/%s: %w", volumeName, namespace, vmName, err)
	}
	return nil
}

func hotplugPVCToVM(
	virtClient kubecli.KubevirtClient, namespace, vmName, volumeName, pvcName string,
) error {
	vm, err := virtClient.VirtualMachine(namespace).Get(context.Background(), vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	vm.Spec.Template.Spec.Volumes = append(vm.Spec.Template.Spec.Volumes, kubevirtv1.Volume{
		Name: volumeName,
		VolumeSource: kubevirtv1.VolumeSource{
			PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{
				PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName},
				Hotpluggable:                      true,
			},
		},
	})
	vm.Spec.Template.Spec.Domain.Devices.Disks = append(vm.Spec.Template.Spec.Domain.Devices.Disks, kubevirtv1.Disk{
		Name:       volumeName,
		DiskDevice: kubevirtv1.DiskDevice{Disk: &kubevirtv1.DiskTarget{Bus: kubevirtv1.DiskBusSCSI}},
		Serial:     controller.GetDiskSerial(volumeName),
	})
	_, err = virtClient.VirtualMachine(namespace).Update(context.Background(), vm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("hotplug PVC %s as volume %s: %w", pvcName, volumeName, err)
	}
	return nil
}

func waitForVMIVolumeDetached(
	virtClient kubecli.KubevirtClient, namespace, vmiName, volumeName string, timeout time.Duration,
) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		vmi, err := virtClient.VirtualMachineInstance(namespace).Get(
			context.Background(), vmiName, metav1.GetOptions{},
		)
		if err != nil {
			return err
		}
		attached := false
		for _, volume := range vmi.Spec.Volumes {
			if volume.Name == volumeName {
				attached = true
				break
			}
		}
		for _, disk := range vmi.Spec.Domain.Devices.Disks {
			if disk.Name == volumeName {
				attached = true
				break
			}
		}
		for _, status := range vmi.Status.VolumeStatus {
			if status.Name == volumeName {
				attached = true
				break
			}
		}
		if !attached {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("volume %s remained attached to VMI %s/%s", volumeName, namespace, vmiName)
}

func waitForGuestBlockDeviceDetached(
	vmiName, namespace, device, identityFile string, timeout time.Duration,
) error {
	devicePath := "/dev/" + device
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := runSSHCommand(vmiName, namespace,
			fmt.Sprintf("test ! -b %s", shellEscape(devicePath)), identityFile)
		if err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("block device %s remained accessible in guest %s/%s", devicePath, namespace, vmiName)
}

func createLargeFileOnVM(vmiName, namespace, path string, sizeBytes int64, identityFile string) {
	const mebibyte = int64(1024 * 1024)
	blockCount := (sizeBytes + mebibyte - 1) / mebibyte
	command := fmt.Sprintf(
		"dd if=/dev/urandom of=%s bs=1M count=%d iflag=fullblock status=none && sync",
		shellEscape(path), blockCount,
	)
	_, err := runSSHCommandWithTimeout(vmiName, namespace, command, identityFile, 10*time.Minute)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create large file at %s", path)
}

func prepareBootDiskRestoreSnapshot(env *TestEnv, snapName, dataPath, dataFile, content string) {
	_, err := runSSHCommand(vmName, env.Namespace,
		fmt.Sprintf("mkdir -p %s && echo %s > %s && sync",
			shellEscape(dataPath), shellEscape(content), shellEscape(dataFile)), env.PrivateKeyPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to write test data on guest")
	err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, snapName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to create boot disk VolumeSnapshot")
	waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, snapName)
	_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("rm -f %s", shellEscape(dataFile)), env.PrivateKeyPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to delete live test file before restore")
}

var expectedRestorePhaseOrder = []filerestorev1alpha1.RestorePhase{
	filerestorev1alpha1.RestorePhaseHotplugging,
	filerestorev1alpha1.RestorePhaseWaitingForAttachment,
	filerestorev1alpha1.RestorePhaseSSHConnecting,
	filerestorev1alpha1.RestorePhaseRestoring,
	filerestorev1alpha1.RestorePhaseCleanup,
	filerestorev1alpha1.RestorePhaseSucceeded,
}

type phaseWatcher struct {
	mu     sync.Mutex
	phases []filerestorev1alpha1.RestorePhase
	cancel context.CancelFunc
	done   chan struct{}
}

func (w *phaseWatcher) snapshot() []filerestorev1alpha1.RestorePhase {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]filerestorev1alpha1.RestorePhase(nil), w.phases...)
}

func (w *phaseWatcher) close() {
	w.cancel()
	gomega.Eventually(w.done, 30*time.Second).Should(gomega.BeClosed())
}

func (w *phaseWatcher) waitForCompletion() {
	gomega.Eventually(w.done, 30*time.Second).Should(gomega.BeClosed(),
		"phase watch did not observe a terminal restore phase")
}

func startRestorePhaseWatcher(crClient client.WithWatch, namespace, name string) *phaseWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := crClient.Watch(ctx, &filerestorev1alpha1.VirtualMachineFileRestoreList{},
		client.InNamespace(namespace), client.MatchingFields{"metadata.name": name})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to watch restore phases")

	watcher := &phaseWatcher{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(watcher.done)
		var last filerestorev1alpha1.RestorePhase
		for event := range stream.ResultChan() {
			restore, ok := event.Object.(*filerestorev1alpha1.VirtualMachineFileRestore)
			if !ok || restore.Name != name || restore.Status.Phase == "" {
				continue
			}
			phase := restore.Status.Phase
			if phase != last {
				watcher.mu.Lock()
				watcher.phases = append(watcher.phases, phase)
				watcher.mu.Unlock()
				last = phase
			}
			if phase == filerestorev1alpha1.RestorePhaseSucceeded ||
				phase == filerestorev1alpha1.RestorePhaseFailed {
				return
			}
		}
	}()
	return watcher
}

func assertExpectedRestorePhases(observed []filerestorev1alpha1.RestorePhase) {
	gomega.Expect(observed).To(gomega.Equal(expectedRestorePhaseOrder),
		"restore should progress through every expected persisted phase in order")
}

func formatDataDisk(vmiName, namespace, diskName, mountPoint, fstype, identityFile string) {
	device := waitForDataDiskDevice(vmiName, namespace, diskName, identityFile)
	setupScript := fmt.Sprintf(`set -ex
parted -s /dev/%s mklabel gpt
parted -s /dev/%s mkpart primary 1MiB 100%%
sleep 2
partprobe /dev/%s
sleep 2
PART=$(lsblk -ln -o NAME /dev/%s | tail -1)
mkfs.%s /dev/$PART
mkdir -p %s
mount /dev/$PART %s
sync
`, device, device, device, device, fstype, shellEscape(mountPoint), shellEscape(mountPoint))
	_, err := runSSHCommandWithTimeout(vmiName, namespace, setupScript, identityFile, 5*time.Minute)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to format %s disk %s", fstype, diskName)
}

func waitForDataDiskDevice(vmiName, namespace, diskName, identityFile string) string {
	var device string
	gomega.Eventually(func(g gomega.Gomega) {
		var err error
		device, err = findDataDiskDevice(vmiName, namespace, diskName, identityFile)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(device).NotTo(gomega.BeEmpty(), "Data disk %s not visible in guest", diskName)
	}, 2*time.Minute, 5*time.Second).Should(gomega.Succeed())
	return device
}

func findDataDiskDevice(vmiName, namespace, diskName, identityFile string) (string, error) {
	output, err := runSSHCommand(vmiName, namespace, "lsblk -d -n -o NAME,SERIAL,TYPE", identityFile)
	if err != nil {
		return "", err
	}
	serial := controller.GetDiskSerial(diskName)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && (fields[1] == diskName || fields[1] == serial) && fields[2] == "disk" {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("data disk %s not found by serial", diskName)
}

func assertFilerestoreSSHAuth(
	restore *filerestorev1alpha1.VirtualMachineFileRestore,
	vmiName, namespace, identityFile string,
) {
	gomega.Expect(restore.Status.StartTime).NotTo(gomega.BeNil(), "restore startTime not set")
	gomega.Expect(restore.Status.CompletionTime).NotTo(gomega.BeNil(), "restore completionTime not set")

	journalCmd := "journalctl --since \"@%d\" --until \"@%d\" --no-pager 2>/dev/null | " +
		"grep -i 'accepted publickey for filerestore' || true"
	cmd := fmt.Sprintf(journalCmd, restore.Status.StartTime.Unix(), restore.Status.CompletionTime.Add(time.Second).Unix())
	out, err := runSSHCommand(vmiName, namespace, cmd, identityFile)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Failed to read SSH auth log on guest")
	gomega.Expect(strings.TrimSpace(out)).NotTo(gomega.BeEmpty(),
		"SSH authentication log should show the operator connected as filerestore")
}
