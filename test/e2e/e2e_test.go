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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	filerestorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
	"kubevirt.io/vm-file-restore-operator/test/utils"
)

const (
	serviceAccountName     = "vm-file-restore-controller-manager"
	metricsServiceName     = "vm-file-restore-controller-manager-metrics-service"
	metricsRoleBindingName = "vm-file-restore-metrics-binding"

	snapshotName  = "fedora-file-restore-test-snap"
	restoreCRName = "restore-test-donald-home"
	testUser      = "donald"
	testFilePath  = "/home/donald/testfile.dat"
)

var _ = Describe("Manager", Ordered, func() {
	// Operator and namespace are already deployed via 'make cluster-sync'
	// Just verify they are present
	BeforeAll(func() {
		By("verifying namespace exists")
		cmd := exec.Command("kubectl", "get", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Namespace '%s' not found. Run 'make cluster-sync' first.", namespace)

		By("verifying CRDs are installed")
		cmd = exec.Command("kubectl", "get", "crd", "virtualmachinefilerestores.filerestore.kubevirt.io")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "CRDs not found. Run 'make cluster-sync' first.")

		By("verifying FileRestoreOperator CRD is installed")
		cmd = exec.Command("kubectl", "get", "crd", "filerestoreoperators.filerestore.kubevirt.io")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "FileRestoreOperator CRD not found. Run 'make cluster-sync' first.")
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
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			if err != nil && !strings.Contains(err.Error(), "already exists") {
				Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")
			}

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken(namespace, serviceAccountName)
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
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
				}`, token, metricsServiceName, namespace, serviceAccountName))
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
		// 1. Create a Fedora VM with a boot disk (setupTestVM)
		// 2. Create test user and generate test file
		// 3. Snapshot the boot disk
		// 4. Delete the test file, create VirtualMachineFileRestore CR
		// 5. Verify file is restored with correct size and ownership
		It("should restore files from VolumeSnapshot to VM", func() {
			env := setupTestVM("e2e-filerestore")

			By("creating test user")
			useraddCmd := fmt.Sprintf("useradd -m -s /bin/bash %s", testUser)
			_, err := runSSHCommand(vmName, env.Namespace, useraddCmd, env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test user")

			By("creating test file")
			createFileCmd := fmt.Sprintf("su - %s -c 'fallocate -l 1G %s && sync'", testUser, testFilePath)
			_, err = runSSHCommand(vmName, env.Namespace, createFileCmd, env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test file")

			By("syncing filesystem to ensure data is written")
			_, err = runSSHCommand(vmName, env.Namespace, "sync", env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to sync filesystem")
			time.Sleep(3 * time.Second)

			By("recording original file size")
			originalSize, err := getFileSizeFromVM(vmName, env.Namespace, testFilePath, env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to get file size")
			Expect(originalSize).To(BeNumerically(">", 0), "File is empty")

			By("creating VolumeSnapshot of boot disk")
			err = createVolumeSnapshot(env.SnapshotClient, env.K8sClient, env.Namespace, bootDiskName, snapshotName)
			Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshot")

			By("waiting for VolumeSnapshot to be ready")
			Eventually(func(g Gomega) {
				snapshot, err := env.SnapshotClient.SnapshotV1().VolumeSnapshots(env.Namespace).Get(
					context.Background(), snapshotName, metav1.GetOptions{},
				)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get VolumeSnapshot")
				g.Expect(snapshot.Status).NotTo(BeNil(), "VolumeSnapshot has no status")
				g.Expect(snapshot.Status.ReadyToUse).NotTo(BeNil(), "VolumeSnapshot ReadyToUse is nil")
				g.Expect(*snapshot.Status.ReadyToUse).To(BeTrue(), "VolumeSnapshot not ready")
			}, 3*time.Minute, 10*time.Second).Should(Succeed())

			By("deleting the test file to verify restore")
			_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("rm -f %s", testFilePath), env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test file")

			By("verifying file is actually deleted")
			_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test ! -f %s", testFilePath), env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "File was not deleted")

			By("creating VirtualMachineFileRestore CR")
			err = createFileRestoreCR(
				env.CRClient, env.Namespace, restoreCRName, vmName, snapshotName, fmt.Sprintf("/home/%s", testUser),
			)
			Expect(err).NotTo(HaveOccurred(), "Failed to create restore CR")

			By("waiting for restore to complete")
			Eventually(func(g Gomega) {
				restore := &filerestorev1alpha1.VirtualMachineFileRestore{}
				err := env.CRClient.Get(context.Background(),
					client.ObjectKey{Namespace: env.Namespace, Name: restoreCRName}, restore)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get restore CR")
				g.Expect(restore.Status.Phase).To(Equal(filerestorev1alpha1.RestorePhaseSucceeded),
					fmt.Sprintf("Restore phase is %s", restore.Status.Phase))
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

			By("verifying restored file exists")
			_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test -f %s", testFilePath), env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Restored file does not exist")

			By("verifying restored file size matches original")
			restoredSize, err := getFileSizeFromVM(vmName, env.Namespace, testFilePath, env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to get restored file size")
			Expect(restoredSize).To(Equal(originalSize), "File size mismatch")

			By("verifying file ownership")
			owner, err := runSSHCommand(vmName, env.Namespace, fmt.Sprintf("stat -c %%U %s", testFilePath), env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to check file owner")
			Expect(owner).To(Equal(testUser), "File ownership incorrect")

			By("verifying file is readable")
			readOutput, err := runSSHCommand(vmName, env.Namespace,
				fmt.Sprintf("test -r %s && echo readable || echo unreadable", testFilePath), env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to check file readability")
			Expect(readOutput).To(ContainSubstring("readable"), "File is not readable")
		})

		// Test manual restore mode where sourcePath is omitted
		It("should support manual restore mode with volume hotplug", func() {
			env := setupTestVM("e2e-manual-restore")

			By(fmt.Sprintf("creating test user %s", testUser))
			useraddCmd := fmt.Sprintf("useradd -m -s /bin/bash %s", testUser)
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
			Eventually(func(g Gomega) {
				snapshot, err := env.SnapshotClient.SnapshotV1().VolumeSnapshots(env.Namespace).Get(
					context.Background(), snapshotName, metav1.GetOptions{},
				)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get VolumeSnapshot")
				g.Expect(snapshot.Status).NotTo(BeNil(), "VolumeSnapshot has no status")
				g.Expect(snapshot.Status.ReadyToUse).NotTo(BeNil(), "VolumeSnapshot ReadyToUse is nil")
				g.Expect(*snapshot.Status.ReadyToUse).To(BeTrue(), "VolumeSnapshot not ready")
			}, 3*time.Minute, 10*time.Second).Should(Succeed())

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
						APIGroup: func() *string { s := "kubevirt.io"; return &s }(),
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
			var mountPath string
			Eventually(func(g Gomega) {
				restore := &filerestorev1alpha1.VirtualMachineFileRestore{}
				err := env.CRClient.Get(context.Background(),
					client.ObjectKey{Namespace: env.Namespace, Name: manualRestoreName}, restore)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get restore CR")
				g.Expect(restore.Status.Phase).To(Equal(filerestorev1alpha1.RestorePhaseVolumeReady),
					fmt.Sprintf("Restore phase is %s (expected VolumeReady)", restore.Status.Phase))
				g.Expect(restore.Status.MountPath).NotTo(BeEmpty(), "MountPath not set")
				mountPath = restore.Status.MountPath
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

			Expect(mountPath).To(Equal("/backup-"+snapshotName), "MountPath format incorrect")

			By("verifying files are accessible from snapshot")
			snapshotFilePath := mountPath + manualTestFile
			Eventually(func(g Gomega) {
				_, err := runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test -d %s", mountPath), env.PrivateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "Mount point does not exist")
				_, err = runSSHCommand(vmName, env.Namespace, fmt.Sprintf("test -f %s", snapshotFilePath), env.PrivateKeyPath)
				g.Expect(err).NotTo(HaveOccurred(), "File not accessible from snapshot mount")
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying file content from snapshot")
			content, err := runSSHCommand(vmName, env.Namespace, fmt.Sprintf("cat %s", snapshotFilePath), env.PrivateKeyPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to read file from snapshot")
			Expect(content).To(ContainSubstring(testFileContent), "File content mismatch")

			By("deleting VirtualMachineFileRestore CR to unplug volume")
			err = env.CRClient.Delete(context.Background(), manualRestore)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete restore CR")

			By("verifying volume is unmounted after CR deletion")
			Eventually(func(g Gomega) {
				_, err := runSSHCommand(vmName, env.Namespace, fmt.Sprintf("mountpoint -q %s", mountPath), env.PrivateKeyPath)
				g.Expect(err).To(HaveOccurred(), "Volume still mounted after CR deletion")
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

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

			Eventually(func(g Gomega) {
				snapshot, err := env.SnapshotClient.SnapshotV1().VolumeSnapshots(env.Namespace).Get(
					context.Background(), lvmSnapshotName, metav1.GetOptions{},
				)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get VolumeSnapshot")
				g.Expect(snapshot.Status).NotTo(BeNil(), "VolumeSnapshot has no status")
				g.Expect(snapshot.Status.ReadyToUse).NotTo(BeNil(), "VolumeSnapshot ReadyToUse is nil")
				g.Expect(*snapshot.Status.ReadyToUse).To(BeTrue(), "VolumeSnapshot not ready")
			}, 3*time.Minute, 10*time.Second).Should(Succeed())

			By("creating automatic-mode VirtualMachineFileRestore CR for LVM snapshot")
			err = createFileRestoreCR(env.CRClient, env.Namespace, lvmRestoreName, vmName, lvmSnapshotName, lvmSourcePath)
			Expect(err).NotTo(HaveOccurred(), "Failed to create LVM restore CR")

			By("waiting for restore to complete")
			Eventually(func(g Gomega) {
				restore := &filerestorev1alpha1.VirtualMachineFileRestore{}
				err := env.CRClient.Get(context.Background(),
					client.ObjectKey{Namespace: env.Namespace, Name: lvmRestoreName}, restore)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get restore CR")
				g.Expect(restore.Status.Phase).To(Equal(filerestorev1alpha1.RestorePhaseSucceeded),
					fmt.Sprintf("Restore phase is %s, error: %s", restore.Status.Phase, restore.Status.ErrorMessage))
			}, 5*time.Minute, 10*time.Second).Should(Succeed())

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
	})

	Context("FileRestoreOperator", func() {
		It("should create and reconcile FileRestoreOperator CR", func() {
			By("creating FileRestoreOperator CR")
			cmd := exec.Command("kubectl", "apply", "-f",
				"config/samples/restore_v1alpha1_filerestoreoperator.yaml")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(),
				"Failed to create FileRestoreOperator CR")

			By("verifying FileRestoreOperator CR exists")
			verifyFileRestoreOperatorExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "filerestoreoperator",
					"vm-file-restore-operator", "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(),
					"FileRestoreOperator 'vm-file-restore-operator' not found in namespace '%s'", namespace)
			}
			Eventually(verifyFileRestoreOperatorExists).Should(Succeed())

			By("verifying FileRestoreOperator status phase is updated")
			verifyFileRestoreOperatorPhase := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "filerestoreoperator",
					"vm-file-restore-operator", "-n", namespace,
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
					"vm-file-restore-operator", "-n", namespace,
					"-o", "jsonpath={.metadata.generation}")
				generation, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				// Get the status's ObservedGeneration field
				cmd = exec.Command("kubectl", "get", "filerestoreoperator",
					"vm-file-restore-operator", "-n", namespace,
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
				"vm-file-restore-operator", "-n", namespace,
				"-o", "jsonpath={.status.phase}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("Deployed"),
				"FileRestoreOperator should remain in Deployed state")
		})
	})
})
