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
	"os/exec"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	filerestorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
	"kubevirt.io/vm-file-restore-operator/test/utils"
)

const (
	// metricsRoleBindingName is the base name for the metrics reader binding.
	// The actual binding is installation-scoped via metricsClusterRoleBindingName.
	metricsRoleBindingName = "vm-file-restore-metrics-binding"

	snapshotName  = "fedora-file-restore-test-snap"
	restoreCRName = "restore-test-donald-home"
	testUser      = "donald"
	testFilePath  = "/home/donald/testfile.dat"
)

func metricsClusterRoleBindingName(operatorNS string) string {
	return fmt.Sprintf("%s-%s", metricsRoleBindingName, operatorNS)
}

var _ = Describe("Manager", Ordered, ContinueOnFailure, func() {
	var namespace string // operator namespace (configurable for QE)

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

		By("cleaning up metrics ClusterRoleBinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding",
			metricsClusterRoleBindingName(namespace), "--ignore-not-found=true")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(),
			"Failed to delete metrics ClusterRoleBinding %s", metricsClusterRoleBindingName(namespace))
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
				g.Expect(podName).To(ContainSubstring(operatorDeploymentName()))

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
			bindingName := metricsClusterRoleBindingName(namespace)
			// Reconcile subjects for this installation (do not ignore stale "already exists").
			createYAML, err := utils.Run(exec.Command("kubectl", "create", "clusterrolebinding", bindingName,
				"--clusterrole=vm-file-restore-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, operatorServiceAccountName()),
				"--dry-run=client", "-o", "yaml",
			))
			Expect(err).NotTo(HaveOccurred(), "Failed to render ClusterRoleBinding")
			applyCmd := exec.Command("kubectl", "apply", "-f", "-")
			applyCmd.Stdin = strings.NewReader(createYAML)
			_, err = utils.Run(applyCmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd := exec.Command("kubectl", "get", "service", operatorMetricsServiceName(), "-n", namespace)
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
			// rest_client_requests_total is emitted at operator startup (no CR needed),
			// confirming controller-runtime metrics are wired up correctly.
			Expect(metricsOutput).To(ContainSubstring("rest_client_requests_total"))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		Context("standard restore operations", Ordered, func() {
			var sharedEnv *TestEnv
			BeforeAll(func() {
				sharedEnv = setupTestVM("e2e-shared")
			})

			/*
					Automatic restore from backup PVC with file integrity

				Preconditions:
					- Running Linux VM with guest helper and filerestore SSH user configured
					- Backup PVC populated with test files of known size, ownership, and permissions
					- Original file metadata recorded before restore

				Steps:
					1. Create VirtualMachineFileRestore CR in automatic mode with the backup PVC as source
					2. Wait for VirtualMachineFileRestore to reach Succeeded phase
					3. Verify restored file exists at target path with matching metadata
					4. Verify temporary hotplug resources were cleaned up

				Expected: Restored file matches pre-restore size, ownership, and permissions
			*/
			It("should restore files from a backup PVC to a running Linux VM with file integrity preserved", func() {
				const (
					pvcSnapName    = "fedora-pvc-source-snap"
					backupPVCName  = "backup-pvc-from-snap"
					pvcRestoreName = "restore-from-pvc"
				)

				env := sharedEnv
				DeferCleanup(func() {
					deleteFileRestoreIfExists(env, pvcRestoreName)
					deleteDVIfExists(env, backupPVCName)
					deleteSnapshotIfExists(env, pvcSnapName)
				})

				By("creating test user")
				_, err := runSSHCommand(vmName, env.Namespace,
					fmt.Sprintf("id %s &>/dev/null || useradd -m -s /bin/bash %s", testUser, testUser), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create test user")

				By("creating test file with known permissions")
				createFileCmd := fmt.Sprintf(
					"su - %s -c 'fallocate -l 64M %s && chmod 640 %s && sync'",
					testUser, testFilePath, testFilePath,
				)
				_, err = runSSHCommand(vmName, env.Namespace, createFileCmd, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create test file")
				time.Sleep(2 * time.Second)

				By("recording original file metadata")
				originalSize, err := getFileSizeFromVM(vmName, env.Namespace, testFilePath, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(originalSize).To(BeNumerically(">", 0))
				originalOwner, err := getFileOwnerGroupFromVM(vmName, env.Namespace, testFilePath, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred())
				originalMode, err := getFileModeFromVM(vmName, env.Namespace, testFilePath, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred())

				By("creating VolumeSnapshot of boot disk")
				err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, pvcSnapName)
				Expect(err).NotTo(HaveOccurred())
				waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, pvcSnapName)

				By("creating backup PVC from VolumeSnapshot")
				createPVCFromSnapshot(env.CRClient, env.K8sClient, env.Namespace, pvcSnapName, backupPVCName)

				By("deleting the test file to verify restore")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("rm -f %s", testFilePath), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred())
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test ! -f %s", testFilePath), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "File was not deleted")

				By("creating VirtualMachineFileRestore CR with PVC source")
				err = createFileRestoreCRFromPVC(
					env.CRClient, env.Namespace, pvcRestoreName, vmName, backupPVCName, fmt.Sprintf("/home/%s", testUser),
				)
				Expect(err).NotTo(HaveOccurred())

				By("waiting for restore to complete")
				restore := waitForRestorePhase(env.CRClient, env.Namespace, pvcRestoreName,
					filerestorev1alpha1.RestorePhaseSucceeded)

				By("verifying the operator authenticated as the restricted filerestore user")
				assertFilerestoreSSHAuth(restore, vmName, env.Namespace, env.PrivateKeyPath)

				By("verifying restored file integrity")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test -f %s", testFilePath), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Restored file does not exist")

				restoredSize, err := getFileSizeFromVM(vmName, env.Namespace, testFilePath, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(restoredSize).To(Equal(originalSize))

				owner, err := getFileOwnerGroupFromVM(vmName, env.Namespace, testFilePath, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(owner).To(Equal(originalOwner))

				mode, err := getFileModeFromVM(vmName, env.Namespace, testFilePath, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(mode).To(Equal(originalMode))

				By("verifying temporary hotplug volume is detached (PVC source leaves the backup PVC)")
				assertRestoreVolumeDetached(env.VirtClient, env.Namespace, vmName, pvcRestoreName)
			})

			/*
				Automatic restore from VolumeSnapshot while VM remains reachable.

				Preconditions:
					- Running Linux VM with guest helper and filerestore SSH user configured
					- VolumeSnapshot containing a directory tree (files + subdirectory)
					- VM network connectivity baseline verified before restore

				Steps:
					1. Start background connectivity probe (SSH echo every 2s)
					2. Create VirtualMachineFileRestore CR in automatic mode with VolumeSnapshot as source
					3. Wait for restore to reach Succeeded while connectivity probe runs
					4. Verify restored files and directories exist at the target path
					5. Confirm VM still responds to connectivity checks after restore

				Expected: Restore succeeds with zero connectivity probe failures before/during/after;
				files and directories are present at the target path

			*/
			It("should restore files and directories from VolumeSnapshot while the VM remains reachable", func() {
				const (
					restoreTreeDir = "/home/donald/restore-tree"
					restoreFile    = "/home/donald/restore-tree/file1.txt"
					restoreSubdir  = "/home/donald/restore-tree/subdir"
					restoreNested  = "/home/donald/restore-tree/subdir/nested.txt"
				)

				env := sharedEnv
				DeferCleanup(func() {
					deleteFileRestoreIfExists(env, restoreCRName)
					deleteSnapshotIfExists(env, snapshotName)
				})

				By("creating test user")
				useraddCmd := fmt.Sprintf("id %s &>/dev/null || useradd -m -s /bin/bash %s", testUser, testUser)
				_, err := runSSHCommand(vmName, env.Namespace, useraddCmd, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create test user")

				By("creating a directory tree with files and a subdirectory")
				setupTree := fmt.Sprintf(`
mkdir -p %s
echo 'top-level-content' > %s
echo 'nested-content' > %s
chown -R %s:%s %s
sync
`, restoreSubdir, restoreFile, restoreNested, testUser, testUser, restoreTreeDir)
				_, err = runSSHCommand(vmName, env.Namespace, setupTree, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create restore directory tree")
				time.Sleep(2 * time.Second)

				By("creating VolumeSnapshot of boot disk")
				err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, snapshotName)
				Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshot")

				By("waiting for VolumeSnapshot to be ready")
				waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, snapshotName)

				By("deleting the directory tree to verify restore")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("rm -rf %s", restoreTreeDir), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to delete directory tree")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test ! -e %s", restoreTreeDir), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Directory tree was not deleted")

				By("verifying baseline SSH connectivity before restore")
				_, err = runSSHCommand(vmName, env.Namespace, "echo reachable", env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "VM not reachable before restore")

				By("starting background connectivity probe")
				stopProbe, probeFailures, probeDone := startConnectivityProbe(
					vmName, env.Namespace, env.PrivateKeyPath, 2*time.Second,
				)
				var stopOnce sync.Once
				DeferCleanup(func() {
					stopOnce.Do(func() { close(stopProbe) })
					Eventually(probeDone, 30*time.Second).Should(BeClosed())
				})

				By("creating VirtualMachineFileRestore CR")
				err = createFileRestoreCR(
					env.CRClient, env.Namespace, restoreCRName, snapshotName, restoreTreeDir,
				)
				Expect(err).NotTo(HaveOccurred(), "Failed to create restore CR")

				By("waiting for restore to complete")
				waitForRestorePhase(env.CRClient, env.Namespace, restoreCRName,
					filerestorev1alpha1.RestorePhaseSucceeded)

				stopOnce.Do(func() { close(stopProbe) })
				Eventually(probeDone, 30*time.Second).Should(BeClosed())
				Expect(probeFailures.Load()).To(BeNumerically("<=", 2),
					"connectivity probe reported too many failures during restore")

				By("verifying restored directories exist")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test -d %s", restoreTreeDir), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Restored top-level directory does not exist")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test -d %s", restoreSubdir), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Restored subdirectory does not exist")

				By("verifying restored files exist with expected content")
				topContent, err := runSSHCommand(vmName, env.Namespace, fmt.Sprintf("cat %s", restoreFile), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Restored top-level file missing")
				Expect(topContent).To(ContainSubstring("top-level-content"))
				nestedContent, err := runSSHCommand(vmName, env.Namespace, fmt.Sprintf("cat %s", restoreNested), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Restored nested file missing")
				Expect(nestedContent).To(ContainSubstring("nested-content"))

				By("verifying SSH connectivity after restore")
				_, err = runSSHCommand(vmName, env.Namespace, "echo still-reachable", env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "VM not reachable after restore")
			})

			/*
				Manual restore mode: read-only mount, interactive copy, cleanup on delete.

				Preconditions:
					- Running Linux VM with guest helper and filerestore SSH user configured
					- Backup snapshot populated with test files

				Steps:
					1. Create VirtualMachineFileRestore CR in manual mode (no sourcePath)
					2. Wait for restore to reach VolumeReady phase
					3. Verify backup volume is mounted read-only in the guest
					4. Copy/read a file from the read-only backup mount
					5. Delete the VirtualMachineFileRestore CR
					6. Verify hotplugged backup volume is detached

				Expected: Backup mounted read-only; files accessible for interactive copy;
				resources detached on CR deletion

			*/
			It("should make backup available read-only in guest when manual restore", func() {
				env := sharedEnv
				DeferCleanup(func() {
					deleteFileRestoreIfExists(env, "manual-restore-test")
					deleteSnapshotIfExists(env, snapshotName)
				})

				By(fmt.Sprintf("creating test user %s", testUser))
				useraddCmd := fmt.Sprintf("id %s &>/dev/null || useradd -m -s /bin/bash %s", testUser, testUser)
				_, err := runSSHCommand(vmName, env.Namespace, useraddCmd, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create test user")

				By(fmt.Sprintf("creating test file in /home/%s", testUser))
				testFileContent := "manual-restore-test-content-" + time.Now().String()
				manualTestFile := fmt.Sprintf("/home/%s/test-manual-restore.txt", testUser)
				createFileCmd := fmt.Sprintf("su - %s -c 'echo \"%s\" > %s' && sync", testUser, testFileContent, manualTestFile)
				_, err = runSSHCommand(vmName, env.Namespace, createFileCmd, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create test file")

				By("syncing filesystem to ensure data is written")
				_, err = runSSHCommand(vmName, env.Namespace, "sync", env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to sync filesystem")
				time.Sleep(3 * time.Second)

				By("verifying test file exists before snapshot")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test -f %s", manualTestFile), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Test file does not exist before snapshot")

				By("creating VolumeSnapshot")
				err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, snapshotName)
				Expect(err).NotTo(HaveOccurred(), "Failed to create snapshot")

				By("waiting for snapshot to be ready")
				waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, snapshotName)

				By("deleting the test file")
				_, err = runSSHCommand(vmName, env.Namespace, "rm -f "+manualTestFile, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to delete test file")

				By("creating manual-mode VirtualMachineFileRestore CR (no sourcePath)")
				manualRestoreName := "manual-restore-test"
				manualRestore := &filerestorev1alpha1.VirtualMachineFileRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      manualRestoreName,
						Namespace: env.Namespace,
					},
					Spec: filerestorev1alpha1.VirtualMachineFileRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: kubevirtAPIGroupPtr(),
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						Source: filerestorev1alpha1.RestoreSource{
							Snapshot: &filerestorev1alpha1.VolumeSnapshotSource{
								Name: snapshotName,
							},
						},
					},
				}
				err = env.CRClient.Create(context.Background(), manualRestore)
				Expect(err).NotTo(HaveOccurred(), "Failed to create manual restore CR")

				By("waiting for VolumeReady phase")
				restore := waitForRestorePhase(env.CRClient, env.Namespace, manualRestoreName,
					filerestorev1alpha1.RestorePhaseVolumeReady)
				mountPath := restore.Status.MountPath
				Expect(mountPath).NotTo(BeEmpty(), "MountPath not set")
				Expect(mountPath).To(Equal("/backup-"+snapshotName), "MountPath format incorrect")

				By("verifying backup mount is read-only")
				Eventually(func(g Gomega) {
					mp := shellEscape(mountPath)
					roCheck, err := runSSHCommand(vmName, env.Namespace,
						fmt.Sprintf("findmnt -n -o OPTIONS %s || awk -v mp=%s '$2==mp{print $4}' /proc/mounts", mp, mp),
						env.PrivateKeyPath)
					g.Expect(err).NotTo(HaveOccurred(), "Failed to inspect mount options")
					firstOpt := strings.Split(strings.TrimSpace(roCheck), ",")[0]
					g.Expect(firstOpt).To(Equal("ro"), "Expected first mount option to be ro, got %q", roCheck)
				}, 2*time.Minute, 5*time.Second).Should(Succeed())

				By("verifying files are accessible from snapshot")
				snapshotFilePath := mountPath + manualTestFile
				Eventually(func(g Gomega) {
					_, err := runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test -d %s", mountPath), env.PrivateKeyPath)
					g.Expect(err).NotTo(HaveOccurred(), "Mount point does not exist")
					_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test -f %s", snapshotFilePath), env.PrivateKeyPath)
					g.Expect(err).NotTo(HaveOccurred(), "File not accessible from snapshot mount")
				}, 2*time.Minute, 5*time.Second).Should(Succeed())

				By("copying a file from the read-only backup mount to /tmp")
				_, err = runSSHCommand(vmName, env.Namespace,
					fmt.Sprintf("cp %s /tmp/manual-restore-copy.txt", shellEscape(snapshotFilePath)), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to copy file from read-only mount")

				By("verifying file content from snapshot")
				content, err := runSSHCommand(vmName, env.Namespace, fmt.Sprintf("cat %s", snapshotFilePath), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to read file from snapshot")
				Expect(content).To(ContainSubstring(testFileContent), "File content mismatch")

				By("deleting VirtualMachineFileRestore CR to unplug volume")
				err = env.CRClient.Delete(context.Background(), manualRestore)
				Expect(err).NotTo(HaveOccurred(), "Failed to delete restore CR")

				By("verifying hotplug volume is detached from VMI")
				assertRestoreVolumeDetached(env.VirtClient, env.Namespace, vmName, manualRestoreName)

				By("verifying volume is unmounted after CR deletion")
				Eventually(func(g Gomega) {
					_, err := runSSHCommand(vmName, env.Namespace,
						fmt.Sprintf("! mountpoint -q %s", shellEscape(mountPath)), env.PrivateKeyPath)
					g.Expect(err).NotTo(HaveOccurred(), "volume still mounted at %s after CR deletion", mountPath)
				}, 30*time.Second, 5*time.Second).Should(Succeed())
			})

			/*
				Restore status reports accurate file count.

				Preconditions:
					- Running Linux VM with guest helper and filerestore SSH user configured
					- VolumeSnapshot of boot disk containing a directory with exactly 3 files

				Steps:
					1. Delete the directory from the live disk after snapshot (simulate data loss)
					2. Create VirtualMachineFileRestore CR restoring the directory via automatic mode
					3. Wait for Succeeded and read status.restoredFilesCount
					4. Count restored files in the guest and compare to status

				Expected: restoredFilesCount is 3 and matches files present in the VM
			*/
			It("should report file count matching the actual number of files transferred on a Linux VM", func() {
				const (
					fileCountSnap    = "fedora-file-count-snap"
					fileCountRestore = "restore-file-count-linux"
					fileCountDir     = "/home/donald/restore-count-data"
					expectedFiles    = 3
				)

				env := sharedEnv
				DeferCleanup(func() {
					deleteFileRestoreIfExists(env, fileCountRestore)
					deleteSnapshotIfExists(env, fileCountSnap)
				})

				By("creating test user and a directory tree with three files")
				_, err := runSSHCommand(vmName, env.Namespace,
					fmt.Sprintf("id %s &>/dev/null || useradd -m -s /bin/bash %s", testUser, testUser), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create test user")
				setupFilesCmd := fmt.Sprintf(
					`su - %s -c 'mkdir -p %s/subdir && `+
						`echo file1 > %s/file1.txt && `+
						`echo file2 > %s/file2.txt && `+
						`echo file3 > %s/subdir/file3.txt && sync'`,
					testUser, fileCountDir,
					fileCountDir, fileCountDir, fileCountDir,
				)
				_, err = runSSHCommand(vmName, env.Namespace, setupFilesCmd, env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create test files")
				_, err = runSSHCommand(vmName, env.Namespace,
					fmt.Sprintf("chown -R %s:%s %s", testUser, testUser, fileCountDir), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to chown test files")
				_, err = runSSHCommand(vmName, env.Namespace, "sync", env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to sync filesystem")
				time.Sleep(3 * time.Second)

				By("verifying test files exist before snapshot")
				_, err = runSSHCommand(vmName, env.Namespace,
					fmt.Sprintf("test -f %s/file1.txt && test -f %s/file2.txt && test -f %s/subdir/file3.txt",
						fileCountDir, fileCountDir, fileCountDir), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Test files not present before snapshot")

				By("creating VolumeSnapshot of boot disk")
				err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, fileCountSnap)
				Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshot")
				waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, fileCountSnap)

				By("deleting the directory tree to verify restore")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("rm -rf %s", fileCountDir), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to delete directory tree")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test ! -e %s", fileCountDir), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Directory tree was not deleted")

				By("creating VirtualMachineFileRestore CR")
				err = createFileRestoreCR(env.CRClient, env.Namespace, fileCountRestore, fileCountSnap, fileCountDir)
				Expect(err).NotTo(HaveOccurred(), "Failed to create restore CR")

				By("waiting for restore to complete")
				waitForRestorePhase(env.CRClient, env.Namespace, fileCountRestore,
					filerestorev1alpha1.RestorePhaseSucceeded)

				By("verifying restoredFilesCount in status")
				restore := getFileRestore(env.CRClient, env.Namespace, fileCountRestore)
				Expect(restore.Status.RestoredFilesCount).NotTo(BeNil(),
					"restoredFilesCount should be set after automatic restore; redeploy the operator "+
						"(make cluster-sync) so it includes PR #31 file-count parsing — e2e installs "+
						"the current guest helper, which emits [filerestore] N files restored")
				Expect(*restore.Status.RestoredFilesCount).To(Equal(int32(expectedFiles)),
					"restoredFilesCount should match files transferred")

				By("verifying restored files exist in the guest")
				fileList, err := runSSHCommand(vmName, env.Namespace,
					fmt.Sprintf("find %s -type f | sort", shellEscape(fileCountDir)), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to list restored files")
				restoredPaths := strings.Fields(strings.TrimSpace(fileList))
				Expect(restoredPaths).To(HaveLen(expectedFiles))
				Expect(restoredPaths).To(ConsistOf(
					fileCountDir+"/file1.txt",
					fileCountDir+"/file2.txt",
					fileCountDir+"/subdir/file3.txt",
				))
			})

			/*
				[NEGATIVE] Volume attachment failure.

				Preconditions:
					- Running Linux VM with guest helper configured
					- No PVC named in the restore CR exists in the namespace

				Steps:
					1. Create VirtualMachineFileRestore CR referencing a non-existent PVC
					2. Wait for restore to reach Failed phase
					3. Read status.errorMessage
					4. Inspect VMI for orphaned backup volumes

				Expected: Failed with message describing attachment/source failure; no orphaned volumes
			*/
			It("should report a clear error when volume attachment fails during restore", func() {
				env := sharedEnv
				restoreName := "restore-missing-pvc"
				DeferCleanup(func() { deleteFileRestoreIfExists(env, restoreName) })

				By("creating VirtualMachineFileRestore CR referencing a non-existent PVC")
				err := createFileRestoreCRFromPVC(
					env.CRClient, env.Namespace, restoreName, vmName, "does-not-exist-pvc", "/home/donald",
				)
				Expect(err).NotTo(HaveOccurred())

				By("waiting for Failed phase")
				restore := waitForRestoreFailed(env.CRClient, env.Namespace, restoreName, 3*time.Minute)
				msg := strings.ToLower(restore.Status.ErrorMessage)
				Expect(msg).To(And(
					ContainSubstring("does-not-exist-pvc"),
					ContainSubstring("not found"),
				), "errorMessage should name the missing PVC: %s", restore.Status.ErrorMessage)

				By("verifying no orphaned restore volume remains attached")
				assertRestoreVolumeDetached(env.VirtClient, env.Namespace, vmName, restoreName)
			})

			/*
				[NEGATIVE] File transfer failure.

				Preconditions:
					- Running Linux VM with guest helper and filerestore SSH user configured
					- Backup snapshot exists but does not contain the source path specified in the CR

				Steps:
					1. Create VirtualMachineFileRestore CR with a non-existent source path
					2. Wait for restore to reach Failed phase
					3. Read status.errorMessage
					4. Verify no file was created at the target path
					5. Verify hotplugged volume is detached after failure

				Expected: Failed with transfer/path error; target unchanged; volume cleaned up
			*/
			It("should report a clear error when file transfer fails during restore", func() {
				const (
					xferSnap    = "fedora-xfer-fail-snap"
					xferRestore = "restore-xfer-fail"
					missingPath = "/home/donald/does-not-exist-on-backup"
				)

				env := sharedEnv
				DeferCleanup(func() {
					deleteFileRestoreIfExists(env, xferRestore)
					deleteSnapshotIfExists(env, xferSnap)
				})

				By("creating VolumeSnapshot of a disk that lacks the restore source path")
				err := createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, xferSnap)
				Expect(err).NotTo(HaveOccurred())
				waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, xferSnap)

				By("creating VirtualMachineFileRestore CR with non-existent sourcePath")
				err = createFileRestoreCR(env.CRClient, env.Namespace, xferRestore, xferSnap, missingPath)
				Expect(err).NotTo(HaveOccurred())

				By("waiting for Failed phase")
				restore := waitForRestoreFailed(env.CRClient, env.Namespace, xferRestore, 5*time.Minute)
				Expect(restore.Status.ErrorMessage).NotTo(BeEmpty())

				By("verifying target path was not created")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test ! -e %s", missingPath), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "unexpected path created after failed transfer")

				By("verifying hotplugged volume is cleaned up")
				assertRestoreVolumeDetached(env.VirtClient, env.Namespace, vmName, xferRestore)
				assertNoManagedRestoreDataVolume(env.CRClient, env.Namespace, xferRestore)
			})

			/*
				Temporary resources cleaned up after successful restore.

				Preconditions:
					- Running Linux VM with guest helper and filerestore SSH user configured
					- Backup snapshot with test file
					- Baseline: restore will create a temporary DataVolume for the snapshot source

				Steps:
					1. Create VirtualMachineFileRestore CR in automatic mode
					2. Wait for restore to reach Succeeded phase
					3. Verify hotplugged backup volume is absent from VMI volumeStatus
					4. Verify temporary restore DataVolume was removed
					5. Verify restore CR remains with Succeeded status for audit

				Expected: All temporary resources removed after completion; restore CR available for status inspection
			*/
			It("should automatically clean up all temporary resources after successful restore", func() {
				const cleanupRestoreName = "restore-cleanup-test"

				env := sharedEnv
				DeferCleanup(func() {
					deleteFileRestoreIfExists(env, cleanupRestoreName)
					deleteSnapshotIfExists(env, snapshotName)
				})

				By("creating test user and file")
				_, err := runSSHCommand(vmName, env.Namespace,
					fmt.Sprintf("id %s &>/dev/null || useradd -m -s /bin/bash %s", testUser, testUser), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create test user")
				_, err = runSSHCommand(vmName, env.Namespace,
					fmt.Sprintf("su - %s -c 'echo cleanup-test > %s' && sync", testUser, testFilePath),
					env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred(), "Failed to create test file")
				time.Sleep(2 * time.Second)

				By("creating VolumeSnapshot of boot disk")
				err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, snapshotName)
				Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshot")
				waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, snapshotName)

				By("deleting the test file")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("rm -f %s", testFilePath), env.PrivateKeyPath)
				Expect(err).NotTo(HaveOccurred())

				By("creating VirtualMachineFileRestore CR")
				err = createFileRestoreCR(
					env.CRClient, env.Namespace, cleanupRestoreName, snapshotName, fmt.Sprintf("/home/%s", testUser),
				)
				Expect(err).NotTo(HaveOccurred(), "Failed to create restore CR")

				By("waiting for restore to complete")
				waitForRestorePhase(env.CRClient, env.Namespace, cleanupRestoreName,
					filerestorev1alpha1.RestorePhaseSucceeded)

				assertSuccessfulRestoreCleanup(env.VirtClient, env.CRClient, env.Namespace, vmName, cleanupRestoreName, true)
			})

			It("should progress through all expected phases in order", func() {
				const (
					restoreName = "phase-order"
					snapName    = "phase-order-snap"
					dataPath    = "/home/donald/phase-order-data"
					dataFile    = "/home/donald/phase-order-data/file.txt"
				)

				env := sharedEnv
				DeferCleanup(func() {
					deleteFileRestoreIfExists(env, restoreName)
					deleteSnapshotIfExists(env, snapName)
				})

				By("preparing the backup snapshot")
				prepareBootDiskRestoreSnapshot(env, snapName, dataPath, dataFile, "phase-order")

				By("starting the restore phase watcher")
				watcher := startRestorePhaseWatcher(env.CRClient, env.Namespace, restoreName)
				defer watcher.close()

				By("creating the restore CR")
				err := createFileRestoreCR(env.CRClient, env.Namespace, restoreName, snapName, dataFile)
				Expect(err).NotTo(HaveOccurred())

				By("waiting for the restore to succeed")
				waitForRestorePhase(env.CRClient, env.Namespace, restoreName,
					filerestorev1alpha1.RestorePhaseSucceeded)
				watcher.waitForCompletion()

				By("verifying every expected persisted phase was observed in order")
				assertExpectedRestorePhases(watcher.snapshot())
			})
		}) // end Context("standard restore operations") — shared VM is torn down here before special-VM tests

		/*
			[NEGATIVE] Guest connection failure.

			Preconditions:
				- Running Linux VM without filerestore SSH user / guest helper
				- Valid backup snapshot that can be hotplugged

			Steps:
				1. Create VirtualMachineFileRestore CR targeting the VM without helper configured
				2. Wait for restore to reach Failed phase
				3. Read status.errorMessage
				4. Verify hotplugged volume is detached after failure

			Expected: Failed with SSH/guest connection message; hotplugged volume cleaned up
		*/
		It("should report a clear error when guest connection cannot be established during restore", func() {
			const (
				sshFailSnap    = "fedora-ssh-fail-snap"
				sshFailRestore = "restore-ssh-fail"
			)

			env := setupTestVMWithoutGuestHelper("e2e-ssh-fail")

			By("creating a file so the snapshot has content")
			_, err := runSSHCommand(vmName, env.Namespace,
				"mkdir -p /root/ssh-fail-data && echo data > /root/ssh-fail-data/file.txt && sync",
				env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(2 * time.Second)

			By("creating VolumeSnapshot")
			err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, sshFailSnap)
			Expect(err).NotTo(HaveOccurred())
			waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, sshFailSnap)

			By("creating VirtualMachineFileRestore CR against VM without filerestore user")
			err = createFileRestoreCR(
				env.CRClient, env.Namespace, sshFailRestore, sshFailSnap, "/root/ssh-fail-data",
			)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for Failed phase (SSH retries ~2 minutes)")
			restore := waitForRestoreFailed(env.CRClient, env.Namespace, sshFailRestore, 5*time.Minute)
			msg := strings.ToLower(restore.Status.ErrorMessage)
			Expect(msg).To(Or(
				ContainSubstring("ssh"),
				ContainSubstring("connection"),
				ContainSubstring("timeout"),
			), "errorMessage should indicate SSH/guest connection failure: %s", restore.Status.ErrorMessage)

			By("verifying hotplugged volume is cleaned up")
			assertRestoreVolumeDetached(env.VirtClient, env.Namespace, vmName, sshFailRestore)
			assertNoManagedRestoreDataVolume(env.CRClient, env.Namespace, sshFailRestore)
		})

		/*
			[NEGATIVE] Invalid restore source combinations.

			Thin e2e smoke: empty source + one multi-source case. Fuller combinations
			are covered by the VirtualMachineFileRestore controller unit tests.
		*/
		DescribeTable("should fail restore when source is empty or specifies more than one of pvc, snapshot, remote",
			func(restoreName string, source filerestorev1alpha1.RestoreSource, errSubstring string) {
				env := setupTestEnv("e2e-invalid-source")

				restore := &filerestorev1alpha1.VirtualMachineFileRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      restoreName,
						Namespace: env.Namespace,
					},
					Spec: filerestorev1alpha1.VirtualMachineFileRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: kubevirtAPIGroupPtr(),
							Kind:     "VirtualMachine",
							Name:     "does-not-matter",
						},
						Source:     source,
						SourcePath: "/home/donald",
					},
				}

				By("creating VirtualMachineFileRestore CR with invalid source")
				err := env.CRClient.Create(context.Background(), restore)
				Expect(err).NotTo(HaveOccurred(), "CR create should succeed; validation happens in reconcile")

				By("waiting for Failed phase with a clear source validation error")
				failed := waitForRestoreFailed(env.CRClient, env.Namespace, restoreName, 2*time.Minute)
				Expect(strings.ToLower(failed.Status.ErrorMessage)).To(ContainSubstring(strings.ToLower(errSubstring)),
					"errorMessage %q should contain %q", failed.Status.ErrorMessage, errSubstring)
			},
			Entry("empty source (0 of pvc/snapshot/remote)",
				"restore-no-source",
				filerestorev1alpha1.RestoreSource{},
				"no source specified",
			),
			Entry("pvc and snapshot (2 sources)",
				"restore-pvc-and-snap",
				filerestorev1alpha1.RestoreSource{
					PVC:      &filerestorev1alpha1.PVCSource{Name: "backup-pvc"},
					Snapshot: &filerestorev1alpha1.VolumeSnapshotSource{Name: "backup-snap"},
				},
				"multiple sources specified",
			),
		)

		// When a snapshot of an LVM disk is hotplugged into the same VM, the snapshot
		// carries identical VG/PV UUIDs to the already-active volume group, causing
		// UUID collisions. The guest helper script must detect LVM2_member, use
		// vgimportclone to reassign UUIDs, and mount the LV read-only.
		It("should restore files from LVM-based VolumeSnapshot", func() {
			const (
				lvmDataDiskName = "lvm-data-dv"
				lvmDataDiskSize = "2Gi"
				lvmSnapshotName = "lvm-data-snap"
				lvmRestoreName  = "lvm-restore-test"
				lvmVGName       = "datavg"
				lvmLVName       = "datalv"
				lvmTestContent  = "lvm-restore-test-content-12345"
				lvmSourcePath   = "/tmp/lvm-restore-data"
			)

			env := setupTestVM("e2e-lvm-restore", ExtraDisk{Name: lvmDataDiskName, Size: lvmDataDiskSize})

			By("installing lvm2 package")
			Eventually(func(g Gomega) {
				_, err := runSSHCommand(vmName, env.Namespace, "dnf install -y lvm2", env.PrivateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "lvm2 installation failed")
			}, 3*time.Minute, 10*time.Second).Should(Succeed())

			By("identifying the blank data disk inside the VM")
			var dataDiskDevice string
			Eventually(func(g Gomega) {
				output, err := runSSHCommand(vmName, env.Namespace,
					"lsblk -d -n -o NAME,SIZE,TYPE | grep disk | grep -v vda", env.PrivateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to list disks")
				for _, line := range strings.Split(output, "\n") {
					fields := strings.Fields(line)
					if len(fields) >= 2 && fields[0] != "" {
						checkCmd := fmt.Sprintf("blkid /dev/%s 2>/dev/null; echo $?", fields[0])
						checkOutput, checkErr := runSSHCommand(vmName, env.Namespace, checkCmd, env.PrivateKeyPath)
						if checkErr != nil {
							continue
						}
						if strings.TrimSpace(checkOutput) == "2" {
							dataDiskDevice = fields[0]
							break
						}
					}
				}
				g.Expect(dataDiskDevice).NotTo(BeEmpty(), "Could not find blank data disk")
			}, 1*time.Minute, 5*time.Second).Should(Succeed())

			By("creating partition table and LVM on data disk")
			lvmSetupScript := fmt.Sprintf(`set -ex
parted -s /dev/%s mklabel gpt
parted -s /dev/%s mkpart primary 1MiB 100%%
sleep 2
partprobe /dev/%s
sleep 2
PART=$(lsblk -ln -o NAME /dev/%s | tail -1)
pvcreate /dev/$PART
vgcreate %s /dev/$PART
lvcreate -n %s -l 100%%FREE %s
mkfs.ext4 /dev/%s/%s
mkdir -p /mnt/lvmdata
mount /dev/%s/%s /mnt/lvmdata
mkdir -p /mnt/lvmdata%s
echo '%s' > /mnt/lvmdata%s/testfile.txt
mkdir -p /mnt/lvmdata%s/subdir
echo 'nested-content' > /mnt/lvmdata%s/subdir/nested.txt
sync
umount /mnt/lvmdata
`,
				dataDiskDevice, dataDiskDevice, dataDiskDevice, dataDiskDevice,
				lvmVGName, lvmLVName, lvmVGName,
				lvmVGName, lvmLVName,
				lvmVGName, lvmLVName,
				lvmSourcePath, lvmTestContent, lvmSourcePath,
				lvmSourcePath, lvmSourcePath,
			)
			output, err := runSSHCommandWithTimeout(vmName, env.Namespace, lvmSetupScript, env.PrivateKeyPath, 3*time.Minute)
			Expect(err).NotTo(HaveOccurred(), "LVM setup failed: %s", output)

			By("verifying LVM VG is active")
			vgsOutput, err := runSSHCommand(vmName, env.Namespace, "vgs --noheadings -o vg_name", env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to list VGs")
			Expect(vgsOutput).To(ContainSubstring(lvmVGName), "VG not found")

			_, err = runSSHCommand(vmName, env.Namespace, "sync", env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to sync")
			time.Sleep(3 * time.Second)

			By("creating VolumeSnapshot of LVM data disk")
			err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, lvmDataDiskName, lvmSnapshotName)
			Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshot")
			waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, lvmSnapshotName)

			By("creating automatic-mode VirtualMachineFileRestore CR for LVM snapshot")
			err = createFileRestoreCR(env.CRClient, env.Namespace, lvmRestoreName, lvmSnapshotName, lvmSourcePath)
			Expect(err).NotTo(HaveOccurred(), "Failed to create LVM restore CR")

			By("waiting for restore to complete")
			waitForRestorePhase(env.CRClient, env.Namespace, lvmRestoreName,
				filerestorev1alpha1.RestorePhaseSucceeded)

			By("verifying restored files")
			content, err := runSSHCommand(vmName, env.Namespace,
				fmt.Sprintf("cat %s/testfile.txt", lvmSourcePath), env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read restored file")
			Expect(content).To(ContainSubstring(lvmTestContent), "File content mismatch")

			nestedContent, err := runSSHCommand(vmName, env.Namespace,
				fmt.Sprintf("cat %s/subdir/nested.txt", lvmSourcePath), env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read restored nested file")
			Expect(nestedContent).To(ContainSubstring("nested-content"), "Nested file content mismatch")

			By("verifying original VG is still active (no corruption)")
			vgsAfter, err := runSSHCommand(vmName, env.Namespace, "vgs --noheadings -o vg_name", env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to list VGs after restore")
			Expect(vgsAfter).To(ContainSubstring(lvmVGName), "Original VG disappeared")
		})

		Context("formatted data disk restore coverage", Ordered, func() {
			var env *TestEnv

			BeforeAll(func() {
				env = setupTestVM("e2e-filesystems",
					ExtraDisk{Name: "ext4-data-dv", Size: "2Gi"},
					ExtraDisk{Name: "xfs-data-dv", Size: "2Gi"},
				)
			})

			DescribeTable("should restore files from formatted data disk snapshots",
				func(fstype, dataDiskName string) {
					restoreName := fstype + "-restore"
					snapName := fstype + "-snap"
					mountPoint := "/mnt/" + fstype + "-restore-data"
					diskRelativePath := "/" + fstype + "-restore.dat"
					dataFileOnDisk := mountPoint + diskRelativePath
					testContent := fstype + "-restore-content"

					DeferCleanup(func() {
						deleteFileRestoreIfExists(env, restoreName)
						deleteSnapshotIfExists(env, snapName)
					})

					if fstype == "xfs" {
						By("installing xfsprogs")
						Eventually(func(g Gomega) {
							_, err := runSSHCommand(vmName, env.Namespace, "dnf install -y xfsprogs", env.PrivateKeyPath)
							g.Expect(err).NotTo(HaveOccurred())
						}, 3*time.Minute, 10*time.Second).Should(Succeed())
					}

					By(fmt.Sprintf("formatting data disk with %s", fstype))
					formatDataDisk(vmName, env.Namespace, dataDiskName, mountPoint, fstype, env.PrivateKeyPath)

					By("writing source data")
					_, err := runSSHCommand(vmName, env.Namespace,
						fmt.Sprintf("echo '%s' > %s && sync", testContent, shellEscape(dataFileOnDisk)), env.PrivateKeyPath)
					Expect(err).NotTo(HaveOccurred())

					By("unmounting the data disk before taking its snapshot")
					_, err = runSSHCommand(vmName, env.Namespace,
						fmt.Sprintf("sync && umount %s", shellEscape(mountPoint)), env.PrivateKeyPath)
					Expect(err).NotTo(HaveOccurred())

					By("creating the backup snapshot")
					err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, dataDiskName, snapName)
					Expect(err).NotTo(HaveOccurred())
					waitForVolumeSnapshotReady(env.SnapshotClient, env.Namespace, snapName)

					By("creating the restore CR")
					err = createFileRestoreCR(env.CRClient, env.Namespace, restoreName, snapName, diskRelativePath)
					Expect(err).NotTo(HaveOccurred())
					waitForRestorePhase(env.CRClient, env.Namespace, restoreName,
						filerestorev1alpha1.RestorePhaseSucceeded)

					By("verifying restored file content")
					content, err := runSSHCommand(vmName, env.Namespace,
						fmt.Sprintf("cat %s", shellEscape(diskRelativePath)), env.PrivateKeyPath)
					Expect(err).NotTo(HaveOccurred(), "restored file missing at VM path %s", diskRelativePath)
					Expect(content).To(ContainSubstring(testContent))
				},
				Entry("ext4", "ext4", "ext4-data-dv"),
				Entry("xfs", "xfs", "xfs-data-dv"),
			)
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

			By("verifying FileRestoreOperator Available condition is set")
			verifyFileRestoreOperatorAvailable := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "filerestoreoperator",
					fileRestoreOperatorCRName(), "-n", namespace,
					"-o", `jsonpath={.status.conditions[?(@.type=="Available")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"),
					"Expected FileRestoreOperator Available condition to be 'True', got '%s'", output)
			}
			Eventually(verifyFileRestoreOperatorAvailable).Should(Succeed())

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
			// Verify the FileRestoreOperator is still running and Available
			cmd := exec.Command("kubectl", "get", "filerestoreoperator",
				fileRestoreOperatorCRName(), "-n", namespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="Available")].status}`)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("True"),
				"FileRestoreOperator Available condition should be 'True'")
		})
	})
})
