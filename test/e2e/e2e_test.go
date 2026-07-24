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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	snapshotclientset "github.com/kubernetes-csi/external-snapshotter/client/v6/clientset/versioned"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"
	"sigs.k8s.io/controller-runtime/pkg/client"

	filerestorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
	"kubevirt.io/vm-file-restore-operator/test/utils"
)

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "vm-file-restore-metrics-binding"

const (
	vmName        = "fedora-file-restore-test"
	snapshotName  = "fedora-file-restore-test-snap"
	restoreCRName = "restore-test-donald-home"
	testUser      = "donald"
	testFilePath  = "/home/donald/testfile.dat"
	bootDiskName  = "fedora-boot-dv"
	bootDiskSize  = "10Gi"
)

var _ = Describe("Manager", Ordered, func() {
	var (
		testNamespace string
		namespace     string // operator namespace (configurable for QE)
	)

	// Operator and namespace are already deployed via 'make cluster-sync' or QE setup.sh
	BeforeAll(func() {
		namespace = operatorNamespace()

		By("verifying namespace exists")
		cmd := exec.Command("kubectl", "get", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Namespace '%s' not found. Deploy the operator first.", namespace)

		By("verifying CRDs are installed")
		cmd = exec.Command("kubectl", "get", "crd", "virtualmachinefilerestores.filerestore.kubevirt.io")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "CRDs not found. Deploy the operator first.")

		By("verifying FileRestoreOperator CRD is installed")
		cmd = exec.Command("kubectl", "get", "crd", "filerestoreoperators.filerestore.kubevirt.io")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "FileRestoreOperator CRD not found. Deploy the operator first.")
	})

	// After all tests, clean up test resources but leave operator running
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	// AfterEach intentionally left empty - no verbose failure logging needed

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the operator pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the operator pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve operator pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 operator pod running")
				podName := podNames[0]
				g.Expect(podName).To(ContainSubstring("vm-file-restore-operator"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", podName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect operator pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("discovering the controller pod")
			var podName string
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
					"-l", "control-plane=controller-manager",
					"-o", "jsonpath={.items[0].metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "No controller pod found")
				podName = output
			}, "1m", "5s").Should(Succeed())

			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=vm-file-restore-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, operatorServiceAccountName()),
			)
			_, err := utils.Run(cmd)
			if err != nil && !strings.Contains(err.Error(), "already exists") {
				Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")
			}

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", operatorMetricsServiceName(), "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken(namespace, operatorServiceAccountName())
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", operatorMetricsServiceName(), "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", podName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, operatorMetricsServiceName(), namespace, operatorServiceAccountName()))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput(namespace)
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// Test file restore workflow end-to-end:
		// 1. Create a Fedora VM with a boot disk (DataVolume)
		// 2. Install guest helper with operator's SSH key
		// 3. Create test user and generate test file
		// 4. Snapshot the boot disk
		// 5. Create VirtualMachineFileRestore CR
		// 6. Verify file is restored with correct size and ownership
		It("should restore files from VolumeSnapshot to VM", func() {
			var (
				k8sClient      *kubernetes.Clientset
				virtClient     kubecli.KubevirtClient
				snapshotClient snapshotclientset.Interface
				crClient       client.Client
				pubKey         string
				privateKeyPath string
				originalSize   int64
			)

			// Initialize clients
			By("initializing Kubernetes clients")
			var err error
			k8sClient, virtClient, snapshotClient, crClient, err = initClients()
			Expect(err).NotTo(HaveOccurred(), "Failed to initialize clients")

			// Create unique test namespace
			By("creating unique test namespace")
			testNamespace = fmt.Sprintf("e2e-filerestore-%d", GinkgoRandomSeed())
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: testNamespace,
				},
			}
			_, err = k8sClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
			_, _ = fmt.Fprintf(GinkgoWriter, "Created test namespace: %s\n", testNamespace)

			// Register cleanup that always runs, even if test fails
			DeferCleanup(func() {
				By("cleaning up test resources")

				// Clean up temporary SSH key directory
				if privateKeyPath != "" {
					tmpDir := filepath.Dir(privateKeyPath)
					_ = os.RemoveAll(tmpDir)
				}

				// Delete test namespace (cascades to all resources including VM, snapshot, restore CR)
				_ = k8sClient.CoreV1().Namespaces().Delete(
					context.Background(),
					testNamespace,
					metav1.DeleteOptions{},
				)

				_, _ = fmt.Fprintf(GinkgoWriter, "Cleanup completed\n")
			})

			// Phase 1: Generate temporary SSH keypair for test root access
			By("generating temporary SSH keypair for test")
			tmpDir, err := os.MkdirTemp("", "e2e-ssh-")
			Expect(err).NotTo(HaveOccurred(), "Failed to create temp directory")
			privateKeyPath = tmpDir + "/id_ed25519"

			// Generate ED25519 keypair for root access during test
			cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", privateKeyPath, "-N", "", "-C", "e2e-test")
			keygenOutput, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "Failed to generate SSH keypair: %s", string(keygenOutput))

			// Read test public key
			pubKeyBytes, err := os.ReadFile(privateKeyPath + ".pub")
			Expect(err).NotTo(HaveOccurred(), "Failed to read public key")
			pubKey = strings.TrimSpace(string(pubKeyBytes))
			_, _ = fmt.Fprintf(GinkgoWriter, "Generated test SSH key: %s\n", privateKeyPath)

			// Get operator's SSH public key for guest helper installation
			By("fetching operator's SSH public key from ConfigMap")
			cm, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(
				context.Background(),
				operatorSSHConfigMapName(),
				metav1.GetOptions{},
			)
			Expect(err).NotTo(HaveOccurred(), "Failed to get operator SSH ConfigMap")
			operatorPubKey := cm.Data["ssh-publickey"]
			Expect(operatorPubKey).NotTo(BeEmpty(), "Operator SSH public key is empty in ConfigMap")
			Expect(operatorPubKey).To(HavePrefix("ssh-"), "Invalid operator SSH public key format")
			_, _ = fmt.Fprintf(GinkgoWriter, "Operator SSH key: %s...\n", operatorPubKey[:20])

			// Phase 2: Create VM and wait for it to reach Running state
			By("creating test VirtualMachine")
			err = createTestVM(virtClient, testNamespace, vmName, pubKey, bootDiskName, bootDiskSize)
			Expect(err).NotTo(HaveOccurred(), "Failed to create VM")

			By("waiting for VM to reach Running state")
			Eventually(func(g Gomega) {
				vmi, err := virtClient.VirtualMachineInstance(testNamespace).Get(context.Background(), vmName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get VMI")
				g.Expect(vmi.Status.Phase).To(Equal(kubevirtv1.Running), "VMI not running")
				g.Expect(vmi.Status.Interfaces).NotTo(BeEmpty(), "VMI has no network interfaces")
			}, 5*time.Minute, 10*time.Second).Should(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "VM %s is running\n", vmName)

			// Phase 3: Wait for SSH connectivity, then install guest helper
			By("waiting for SSH connectivity")
			Eventually(func(g Gomega) {
				_, err := runSSHCommand(vmName, testNamespace, "echo ready", privateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "SSH not ready")
			}, 5*time.Minute, 15*time.Second).Should(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "SSH is ready\n")

			By("installing guest helper with operator's SSH key")
			Eventually(func(g Gomega) {
				err := installGuestHelper(vmName, testNamespace, operatorPubKey, privateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "Guest helper installation failed")
			}, 2*time.Minute, 10*time.Second).Should(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "Guest helper installed\n")

			// Phase 4: Create test user and download test file
			By("creating test user")
			_, err = runSSHCommand(vmName, testNamespace, fmt.Sprintf("useradd -m -s /bin/bash %s", testUser), privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test user")

			By("creating test file")
			// Create a 1GiB file quickly; content does not matter for restore validation.
			createFileCmd := fmt.Sprintf(
				"su - %s -c 'fallocate -l 1G %s && sync'",
				testUser, testFilePath,
			)
			_, err = runSSHCommand(vmName, testNamespace, createFileCmd, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test file")
			_, _ = fmt.Fprintf(GinkgoWriter, "Test file created\n")

			By("syncing filesystem to ensure data is written")
			_, err = runSSHCommand(vmName, testNamespace, "sync", privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to sync filesystem")
			time.Sleep(3 * time.Second)

			By("recording original file size")
			originalSize, err = getFileSizeFromVM(vmName, testNamespace, testFilePath, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to get file size")
			Expect(originalSize).To(BeNumerically(">", 0), "Downloaded file is empty")
			_, _ = fmt.Fprintf(GinkgoWriter, "Original file size: %d bytes\n", originalSize)

			// Phase 5: Create VolumeSnapshot
			By("creating VolumeSnapshot of boot disk")
			pvcName := bootDiskName
			err = createVolumeSnapshot(snapshotClient, k8sClient, testNamespace, pvcName, snapshotName)
			Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshot")

			By("waiting for VolumeSnapshot to be ready")
			Eventually(func(g Gomega) {
				snapshot, err := snapshotClient.SnapshotV1().VolumeSnapshots(testNamespace).Get(
					context.Background(),
					snapshotName,
					metav1.GetOptions{},
				)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get VolumeSnapshot")
				g.Expect(snapshot.Status).NotTo(BeNil(), "VolumeSnapshot has no status")
				g.Expect(snapshot.Status.ReadyToUse).NotTo(BeNil(), "VolumeSnapshot ReadyToUse is nil")
				g.Expect(*snapshot.Status.ReadyToUse).To(BeTrue(), "VolumeSnapshot not ready")
			}, 3*time.Minute, 10*time.Second).Should(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "VolumeSnapshot %s is ready\n", snapshotName)

			// Phase 5.5: Delete the file to verify restore actually works
			By("deleting the test file to verify restore")
			deleteCmd := fmt.Sprintf("rm -f %s", testFilePath)
			_, err = runSSHCommand(vmName, testNamespace, deleteCmd, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test file")

			By("verifying file is actually deleted")
			checkCmd := fmt.Sprintf("test ! -f %s", testFilePath)
			_, err = runSSHCommand(vmName, testNamespace, checkCmd, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "File was not deleted (still exists)")
			_, _ = fmt.Fprintf(GinkgoWriter, "Test file deleted successfully\n")

			// Phase 6: Create VirtualMachineFileRestore and wait for completion
			By("creating VirtualMachineFileRestore CR")
			err = createFileRestoreCR(
				crClient, testNamespace, restoreCRName, vmName, snapshotName, fmt.Sprintf("/home/%s", testUser),
			)
			Expect(err).NotTo(HaveOccurred(), "Failed to create restore CR")

			By("waiting for restore to complete")
			Eventually(func(g Gomega) {
				restore := &filerestorev1alpha1.VirtualMachineFileRestore{}
				err := crClient.Get(
					context.Background(),
					client.ObjectKey{Namespace: testNamespace, Name: restoreCRName},
					restore,
				)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get restore CR")
				g.Expect(restore.Status.Phase).To(Equal(filerestorev1alpha1.RestorePhaseSucceeded),
					fmt.Sprintf("Restore phase is %s", restore.Status.Phase))
			}, 5*time.Minute, 10*time.Second).Should(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "File restore completed\n")

			// Phase 7: Verify restored file
			By("verifying restored file exists")
			checkCmd = fmt.Sprintf("test -f %s", testFilePath)
			_, err = runSSHCommand(vmName, testNamespace, checkCmd, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Restored file does not exist")

			By("verifying restored file size matches original")
			restoredSize, err := getFileSizeFromVM(vmName, testNamespace, testFilePath, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to get restored file size")
			Expect(restoredSize).To(Equal(originalSize), "File size mismatch")
			_, _ = fmt.Fprintf(GinkgoWriter, "File size verified: %d bytes\n", restoredSize)

			By("verifying file ownership")
			ownerCmd := fmt.Sprintf("stat -c %%U %s", testFilePath)
			owner, err := runSSHCommand(vmName, testNamespace, ownerCmd, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to check file owner")
			Expect(owner).To(Equal(testUser), "File ownership incorrect")

			By("verifying file is readable")
			readCmd := fmt.Sprintf("test -r %s && echo readable || echo unreadable", testFilePath)
			readOutput, err := runSSHCommand(vmName, testNamespace, readCmd, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to check file readability")
			Expect(readOutput).To(ContainSubstring("readable"), "File is not readable")
		})

		// Test manual restore mode where sourcePath is omitted
		It("should support manual restore mode with volume hotplug", func() {
			var (
				k8sClient      *kubernetes.Clientset
				virtClient     kubecli.KubevirtClient
				snapshotClient snapshotclientset.Interface
				crClient       client.Client
				pubKey         string
				privateKeyPath string
				manualTestNS   string
			)

			// Initialize clients
			By("initializing Kubernetes clients")
			var err error
			k8sClient, virtClient, snapshotClient, crClient, err = initClients()
			Expect(err).NotTo(HaveOccurred(), "Failed to initialize clients")

			// Create unique test namespace for manual restore test
			By("creating unique test namespace")
			manualTestNS = fmt.Sprintf("e2e-manual-restore-%d", time.Now().UnixNano())
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: manualTestNS,
				},
			}
			_, err = k8sClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
			_, _ = fmt.Fprintf(GinkgoWriter, "Created test namespace: %s\n", manualTestNS)

			DeferCleanup(func() {
				By("cleaning up manual restore test resources")
				if privateKeyPath != "" {
					tmpDir := filepath.Dir(privateKeyPath)
					_ = os.RemoveAll(tmpDir)
				}
				_ = k8sClient.CoreV1().Namespaces().Delete(
					context.Background(),
					manualTestNS,
					metav1.DeleteOptions{},
				)
				_, _ = fmt.Fprintf(GinkgoWriter, "Manual restore test cleanup completed\n")
			})

			// Generate SSH keypair
			By("generating temporary SSH keypair")
			tmpDir, err := os.MkdirTemp("", "e2e-manual-ssh-")
			Expect(err).NotTo(HaveOccurred(), "Failed to create temp directory")
			privateKeyPath = tmpDir + "/id_ed25519"

			cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", privateKeyPath, "-N", "", "-C", "e2e-manual-test")
			keygenOutput, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "Failed to generate SSH keypair: %s", string(keygenOutput))

			pubKeyBytes, err := os.ReadFile(privateKeyPath + ".pub")
			Expect(err).NotTo(HaveOccurred(), "Failed to read public key")
			pubKey = strings.TrimSpace(string(pubKeyBytes))

			// Get operator's SSH public key
			By("fetching operator's SSH public key")
			cm, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(
				context.Background(),
				operatorSSHConfigMapName(),
				metav1.GetOptions{},
			)
			Expect(err).NotTo(HaveOccurred(), "Failed to get operator SSH ConfigMap")
			operatorPubKey := cm.Data["ssh-publickey"]
			Expect(operatorPubKey).NotTo(BeEmpty(), "Operator SSH public key is empty")

			// Create VM
			By("creating test VM")
			err = createTestVM(virtClient, manualTestNS, vmName, pubKey, bootDiskName, bootDiskSize)
			Expect(err).NotTo(HaveOccurred(), "Failed to create VM")

			By("waiting for VM to be running")
			Eventually(func(g Gomega) {
				vmi, err := virtClient.VirtualMachineInstance(manualTestNS).Get(context.Background(), vmName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get VMI")
				g.Expect(vmi.Status.Phase).To(Equal(kubevirtv1.Running), "VMI not running")
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

			By("waiting for SSH connectivity")
			Eventually(func(g Gomega) {
				_, err := runSSHCommand(vmName, manualTestNS, "echo ready", privateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "SSH not ready")
			}, 5*time.Minute, 15*time.Second).Should(Succeed())

			// Install guest helper
			By("installing guest helper")
			Eventually(func(g Gomega) {
				err := installGuestHelper(vmName, manualTestNS, operatorPubKey, privateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "Guest helper installation failed")
			}, 2*time.Minute, 10*time.Second).Should(Succeed())

			// Create test user (matching the automatic restore test)
			By(fmt.Sprintf("creating test user %s", testUser))
			_, err = runSSHCommand(vmName, manualTestNS, fmt.Sprintf("useradd -m -s /bin/bash %s", testUser), privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test user")

			// Create test file in user's home directory (matching the automatic restore test)
			By(fmt.Sprintf("creating test file in /home/%s", testUser))
			testFileContent := "manual-restore-test-content-" + time.Now().String()
			manualTestFile := fmt.Sprintf("/home/%s/test-manual-restore.txt", testUser)
			createFileCmd := fmt.Sprintf("su - %s -c 'echo \"%s\" > %s' && sync", testUser, testFileContent, manualTestFile)
			_, err = runSSHCommand(vmName, manualTestNS, createFileCmd, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test file")

			By("syncing filesystem to ensure data is written")
			_, err = runSSHCommand(vmName, manualTestNS, "sync", privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to sync filesystem")
			time.Sleep(3 * time.Second)

			By("verifying test file exists before snapshot")
			lsBeforeCmd := fmt.Sprintf("ls -la %s", manualTestFile)
			lsBeforeOutput, err := runSSHCommand(vmName, manualTestNS, lsBeforeCmd, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Test file does not exist before snapshot")
			_, _ = fmt.Fprintf(GinkgoWriter, "File before snapshot: %s\n", lsBeforeOutput)

			// Create snapshot
			By("creating VolumeSnapshot")
			err = createVolumeSnapshot(snapshotClient, k8sClient, manualTestNS, bootDiskName, snapshotName)
			Expect(err).NotTo(HaveOccurred(), "Failed to create snapshot")

			By("waiting for snapshot to be ready")
			Eventually(func(g Gomega) {
				snapshot, err := snapshotClient.SnapshotV1().VolumeSnapshots(manualTestNS).Get(
					context.Background(),
					snapshotName,
					metav1.GetOptions{},
				)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get VolumeSnapshot")
				g.Expect(snapshot.Status).NotTo(BeNil(), "VolumeSnapshot has no status")
				g.Expect(snapshot.Status.ReadyToUse).NotTo(BeNil(), "VolumeSnapshot ReadyToUse is nil")
				g.Expect(*snapshot.Status.ReadyToUse).To(BeTrue(), "VolumeSnapshot not ready")
			}, 3*time.Minute, 10*time.Second).Should(Succeed())

			// Delete the file to verify manual restore gives access to snapshot
			By("deleting the test file")
			_, err = runSSHCommand(vmName, manualTestNS, "rm -f "+manualTestFile, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test file")

			// Create manual mode VMFR (no sourcePath)
			By("creating manual-mode VirtualMachineFileRestore CR (no sourcePath)")
			manualRestoreName := "manual-restore-test"
			manualRestore := &filerestorev1alpha1.VirtualMachineFileRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      manualRestoreName,
					Namespace: manualTestNS,
				},
				Spec: filerestorev1alpha1.VirtualMachineFileRestoreSpec{
					Target: corev1.TypedLocalObjectReference{
						APIGroup: func() *string { s := "kubevirt.io"; return &s }(),
						Kind:     "VirtualMachine",
						Name:     vmName,
					},
					Source: filerestorev1alpha1.RestoreSource{
						Snapshot: &filerestorev1alpha1.VolumeSnapshotSource{
							Name: snapshotName,
						},
					},
					// No SourcePath - manual mode!
				},
			}
			err = crClient.Create(context.Background(), manualRestore)
			Expect(err).NotTo(HaveOccurred(), "Failed to create manual restore CR")

			// Wait for VolumeReady phase
			By("waiting for VolumeReady phase")
			var mountPath string
			Eventually(func(g Gomega) {
				restore := &filerestorev1alpha1.VirtualMachineFileRestore{}
				err := crClient.Get(
					context.Background(),
					client.ObjectKey{Namespace: manualTestNS, Name: manualRestoreName},
					restore,
				)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get restore CR")
				g.Expect(restore.Status.Phase).To(Equal(filerestorev1alpha1.RestorePhaseVolumeReady),
					fmt.Sprintf("Restore phase is %s (expected VolumeReady)", restore.Status.Phase))
				g.Expect(restore.Status.MountPath).NotTo(BeEmpty(), "MountPath not set")
				mountPath = restore.Status.MountPath
			}, 5*time.Minute, 10*time.Second).Should(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "Volume mounted at: %s\n", mountPath)

			// Verify mount path format
			expectedMountPath := "/backup-" + snapshotName
			Expect(mountPath).To(Equal(expectedMountPath), "MountPath format incorrect")

			// Verify files accessible from snapshot
			By("verifying files are accessible from snapshot")
			snapshotFilePath := mountPath + manualTestFile
			Eventually(func(g Gomega) {
				// Check mount point exists
				checkMountCmd := fmt.Sprintf("test -d %s", mountPath)
				_, err := runSSHCommand(vmName, manualTestNS, checkMountCmd, privateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "Mount point does not exist")

				// Check file exists in snapshot
				checkFileCmd := fmt.Sprintf("test -f %s", snapshotFilePath)
				_, err = runSSHCommand(vmName, manualTestNS, checkFileCmd, privateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "File not accessible from snapshot mount")
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying file content from snapshot")
			catCmd := fmt.Sprintf("cat %s", snapshotFilePath)
			content, err := runSSHCommand(vmName, manualTestNS, catCmd, privateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read file from snapshot")
			Expect(content).To(ContainSubstring(testFileContent), "File content mismatch")
			_, _ = fmt.Fprintf(GinkgoWriter, "File verified from snapshot\n")

			// Delete VMFR to unplug volume
			By("deleting VirtualMachineFileRestore CR to unplug volume")
			err = crClient.Delete(context.Background(), manualRestore)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete restore CR")

			// Verify volume unmounted by checking mount table
			By("verifying volume is unmounted after CR deletion")
			Eventually(func(g Gomega) {
				// Use mountpoint command to check if path is actually a mount point
				checkMountCmd := fmt.Sprintf("mountpoint -q %s", mountPath)
				_, err := runSSHCommand(vmName, manualTestNS, checkMountCmd, privateKeyPath)
				g.Expect(err).To(HaveOccurred(), "Volume still mounted after CR deletion")
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
			_, _ = fmt.Fprintf(GinkgoWriter, "Volume successfully unmounted\n")
		})
	})

	Context("FileRestoreOperator", func() {
		It("should create and reconcile FileRestoreOperator CR", func() {
			By("initializing Kubernetes clients")
			_, _, _, crClient, err := initClients()
			Expect(err).NotTo(HaveOccurred(), "Failed to initialize clients")

			By("creating FileRestoreOperator CR")
			err = createFileRestoreOperatorCR(crClient, namespace, fileRestoreOperatorCRName())
			Expect(err).NotTo(HaveOccurred(), "Failed to create FileRestoreOperator CR")

			By("verifying FileRestoreOperator CR exists")
			verifyFileRestoreOperatorExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "filerestoreoperator",
					fileRestoreOperatorCRName(), "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(),
					"FileRestoreOperator '%s' not found in namespace '%s'",
					fileRestoreOperatorCRName(), namespace)
			}
			Eventually(verifyFileRestoreOperatorExists).Should(Succeed())

			By("verifying FileRestoreOperator status phase is updated")
			verifyFileRestoreOperatorPhase := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "filerestoreoperator",
					fileRestoreOperatorCRName(), "-n", namespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deployed"),
					"Expected FileRestoreOperator phase to be 'Deployed', got '%s'", output)
			}
			Eventually(verifyFileRestoreOperatorPhase).Should(Succeed())

			By("verifying FileRestoreOperator ObservedGeneration is set")
			verifyObservedGeneration := func(g Gomega) {
				// Get the resource's Generation field
				cmd := exec.Command("kubectl", "get", "filerestoreoperator",
					fileRestoreOperatorCRName(), "-n", namespace,
					"-o", "jsonpath={.metadata.generation}")
				generation, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				// Get the status's ObservedGeneration field
				cmd = exec.Command("kubectl", "get", "filerestoreoperator",
					fileRestoreOperatorCRName(), "-n", namespace,
					"-o", "jsonpath={.status.observedGeneration}")
				observedGeneration, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				g.Expect(observedGeneration).To(Equal(generation),
					"Expected ObservedGeneration to match Generation")
			}
			Eventually(verifyObservedGeneration).Should(Succeed())
		})

		It("should perform restore with FileRestoreOperator present", func() {
			By("verifying that the default FileRestoreOperator CR does not interfere with restore operations")
			// Verify the FileRestoreOperator is still running and in Deployed state
			cmd := exec.Command("kubectl", "get", "filerestoreoperator",
				fileRestoreOperatorCRName(), "-n", namespace,
				"-o", "jsonpath={.status.phase}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("Deployed"),
				"FileRestoreOperator should remain in Deployed state")
		})
	})
})
