package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v6/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "kubevirt.io/api/core/v1"
	cdiv1beta1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

// GetVolumeName returns the volume name for a given restore CR name.
// The volume name is used as the volume and disk name; GetDiskSerial derives its guest-detection serial.
// Panics if crName is empty (caller error - should never happen with valid K8s objects).
func GetVolumeName(crName string) string {
	if crName == "" {
		panic("GetVolumeName called with empty crName")
	}
	return crName + "-restore"
}

const (
	maxSCSIDiskSerialLength = 36
	hashedSerialHexLength   = 12
	readableSerialPrefixLen = maxSCSIDiskSerialLength - hashedSerialHexLength - 1
)

// GetDiskSerial returns a stable serial that fits the SCSI serial limit.
// Short volume names are unchanged for compatibility; long names combine a
// readable prefix with a 12-character SHA-256 suffix.
func GetDiskSerial(volumeName string) string {
	if len(volumeName) <= maxSCSIDiskSerialLength {
		return volumeName
	}

	digest := sha256.Sum256([]byte(volumeName))
	return volumeName[:readableSerialPrefixLen] + "-" + hex.EncodeToString(digest[:])[:hashedSerialHexLength]
}

// getSnapshotSourceVolumeMode returns the volume mode recorded on the VolumeSnapshotContent
// bound to the given VolumeSnapshot (spec.sourceVolumeMode). It returns nil when the mode
// cannot be determined - the snapshot has no bound content yet, or sourceVolumeMode is unset -
// so the caller leaves the DataVolume volume mode empty and lets CDI fall back to the default.
// Reads use the uncached reader to avoid starting cluster-wide informers on snapshot resources.
func getSnapshotSourceVolumeMode(ctx context.Context, apiReader client.Reader, snapshotName, snapshotNamespace string) (*corev1.PersistentVolumeMode, error) {
	logger := log.FromContext(ctx)

	snapshot := &snapshotv1.VolumeSnapshot{}
	if err := apiReader.Get(ctx, client.ObjectKey{Name: snapshotName, Namespace: snapshotNamespace}, snapshot); err != nil {
		return nil, fmt.Errorf("failed to get VolumeSnapshot %s/%s: %w", snapshotNamespace, snapshotName, err)
	}

	if snapshot.Status == nil || snapshot.Status.BoundVolumeSnapshotContentName == nil || *snapshot.Status.BoundVolumeSnapshotContentName == "" {
		logger.Info("VolumeSnapshot has no bound VolumeSnapshotContent yet, leaving DataVolume volume mode empty (default)",
			"volumeSnapshot", snapshotName, "namespace", snapshotNamespace)
		return nil, nil
	}
	contentName := *snapshot.Status.BoundVolumeSnapshotContentName

	content := &snapshotv1.VolumeSnapshotContent{}
	if err := apiReader.Get(ctx, client.ObjectKey{Name: contentName}, content); err != nil {
		return nil, fmt.Errorf("failed to get VolumeSnapshotContent %s: %w", contentName, err)
	}

	if content.Spec.SourceVolumeMode == nil || *content.Spec.SourceVolumeMode == "" {
		logger.Info("VolumeSnapshotContent.spec.sourceVolumeMode is not set, leaving DataVolume volume mode empty (default)",
			"volumeSnapshotContent", contentName)
		return nil, nil
	}

	logger.Info("Deriving DataVolume volume mode from VolumeSnapshotContent.spec.sourceVolumeMode",
		"volumeSnapshotContent", contentName, "volumeMode", *content.Spec.SourceVolumeMode)
	return content.Spec.SourceVolumeMode, nil
}

func diskSerialCollision(disks []v1.Disk, volumeName, serial string) string {
	trimmedSerial := strings.TrimSpace(serial)
	for _, disk := range disks {
		if disk.Name != volumeName && strings.EqualFold(strings.TrimSpace(disk.Serial), trimmedSerial) {
			return disk.Name
		}
	}
	return ""
}

func findDiskSerialCollision(vm *v1.VirtualMachine, vmi *v1.VirtualMachineInstance, volumeName, serial string) string {
	if collidingDisk := diskSerialCollision(vm.Spec.Template.Spec.Domain.Devices.Disks, volumeName, serial); collidingDisk != "" {
		return collidingDisk
	}
	if vmi == nil {
		return ""
	}
	return diskSerialCollision(vmi.Spec.Domain.Devices.Disks, volumeName, serial)
}

func readTargetVMAndVMI(ctx context.Context, reader client.Reader, key client.ObjectKey) (*v1.VirtualMachine, *v1.VirtualMachineInstance, error) {
	vm := &v1.VirtualMachine{}
	if err := reader.Get(ctx, key, vm); err != nil {
		return nil, nil, NewTransientError(fmt.Sprintf("failed to re-read target VM %s/%s: %v", key.Namespace, key.Name, err))
	}

	vmi := &v1.VirtualMachineInstance{}
	if err := reader.Get(ctx, key, vmi); err != nil {
		if errors.IsNotFound(err) {
			return vm, nil, nil
		}
		return nil, nil, NewTransientError(fmt.Sprintf("failed to re-read target VMI %s/%s: %v", key.Namespace, key.Name, err))
	}
	return vm, vmi, nil
}

func isRestoreDataVolume(dataVolume *cdiv1beta1.DataVolume, vmfr *restorev1alpha1.VirtualMachineFileRestore) bool {
	return dataVolume.Labels["app.kubernetes.io/managed-by"] == "vm-file-restore-operator" &&
		dataVolume.Labels["filerestore.kubevirt.io/name"] == vmfr.Name
}

func resolveTargetVMForSerialCollision(
	ctx context.Context,
	apiReader client.Reader,
	vmfr *restorev1alpha1.VirtualMachineFileRestore,
) (*v1.VirtualMachine, error) {
	volumeName := GetVolumeName(vmfr.Name)
	serial := GetDiskSerial(volumeName)
	targetKey := client.ObjectKey{Name: vmfr.Spec.Target.Name, Namespace: vmfr.Namespace}

	freshVM, freshVMI, err := readTargetVMAndVMI(ctx, apiReader, targetKey)
	if err != nil {
		return nil, err
	}
	if collidingDisk := findDiskSerialCollision(freshVM, freshVMI, volumeName, serial); collidingDisk != "" {
		return nil, fmt.Errorf("disk serial already in use by disk %s", collidingDisk)
	}

	return freshVM, nil
}

func validateVMHotplugState(vm *v1.VirtualMachine, volumeName string) (bool, error) {
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		if strings.HasSuffix(vol.Name, "-restore") && vol.Name != volumeName {
			return false, fmt.Errorf("another restore is in progress (volume %s exists), cannot hotplug", vol.Name)
		}
	}

	volumeExists := false
	diskExists := false
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		if vol.Name == volumeName {
			volumeExists = true
			break
		}
	}
	for _, disk := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		if disk.Name == volumeName {
			diskExists = true
			break
		}
	}

	if volumeExists && diskExists {
		return true, nil
	}
	if volumeExists || diskExists {
		return false, fmt.Errorf("partial hotplug detected (volume=%v, disk=%v), needs cleanup", volumeExists, diskExists)
	}
	return false, nil
}

// HotplugVolume hotplugs a restore volume to the target VM.
// It handles PVC and snapshot sources, creating temporary PVCs for snapshot sources.
// apiReader is a non-cached reader used for DataVolume status and VM/VMI serial-collision checks.
func HotplugVolume(ctx context.Context, c client.Client, apiReader client.Reader, vmfr *restorev1alpha1.VirtualMachineFileRestore) error {
	logger := log.FromContext(ctx)
	volumeName := GetVolumeName(vmfr.Name)
	serial := GetDiskSerial(volumeName)
	vm, err := resolveTargetVMForSerialCollision(ctx, apiReader, vmfr)
	if err != nil {
		return err
	}

	alreadyHotplugged, err := validateVMHotplugState(vm, volumeName)
	if err != nil {
		return err
	}
	if alreadyHotplugged {
		return nil
	}

	// Build volume source based on the restore source type
	var volumeSource v1.VolumeSource

	if vmfr.Spec.Source.PVC != nil {
		// Validate namespace (KubeVirt doesn't support cross-namespace PVC refs)
		pvcNamespace := vmfr.Spec.Source.PVC.Namespace
		if pvcNamespace == "" {
			pvcNamespace = vmfr.Namespace
		}
		if pvcNamespace != vmfr.Namespace {
			return fmt.Errorf("cross-namespace PVC restore not supported: PVC is in %s, VM is in %s", pvcNamespace, vmfr.Namespace)
		}

		// PVC source: use directly with hotplug
		volumeSource = v1.VolumeSource{
			PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
				PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: vmfr.Spec.Source.PVC.Name,
				},
				Hotpluggable: true,
			},
		}
	} else if vmfr.Spec.Source.Snapshot != nil {
		// Snapshot source: restore the PVC from the snapshot via a DataVolume.
		snapshotNamespace := vmfr.Spec.Source.Snapshot.Namespace
		if snapshotNamespace == "" {
			snapshotNamespace = vmfr.Namespace
		}

		// Read the DataVolume first (uncached, to avoid cache lag). Deriving the volume
		// mode and creating the DataVolume only happens when it doesn't exist yet, so we
		// don't re-read the snapshot resources on every requeue while it provisions.
		dvKey := client.ObjectKey{Name: volumeName, Namespace: vmfr.Namespace}
		dataVolume := &cdiv1beta1.DataVolume{}
		err := apiReader.Get(ctx, dvKey, dataVolume)
		if errors.IsNotFound(err) {
			// Derive the volume mode from the bound VolumeSnapshotContent's sourceVolumeMode.
			// This read is best-effort: if it fails we log and fall back to an empty volume
			// mode (CDI default) rather than failing the restore, so the feature never blocks
			// a restore that would otherwise succeed.
			volumeMode, modeErr := getSnapshotSourceVolumeMode(ctx, apiReader, vmfr.Spec.Source.Snapshot.Name, snapshotNamespace)
			if modeErr != nil {
				logger.Info("Could not determine source volume mode, using CDI default", "error", modeErr)
				volumeMode = nil
			}

			dataVolume = &cdiv1beta1.DataVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:      volumeName,
					Namespace: vmfr.Namespace,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "vm-file-restore-operator",
						"filerestore.kubevirt.io/name": vmfr.Name,
					},
				},
				Spec: cdiv1beta1.DataVolumeSpec{
					Source: &cdiv1beta1.DataVolumeSource{
						Snapshot: &cdiv1beta1.DataVolumeSourceSnapshot{
							Namespace: snapshotNamespace,
							Name:      vmfr.Spec.Source.Snapshot.Name,
						},
					},
					Storage: &cdiv1beta1.StorageSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						VolumeMode: volumeMode,
					},
				},
			}

			// Create DataVolume; re-read after create to observe its status. Tolerate
			// AlreadyExists in case it was created concurrently since the Get above.
			if err := c.Create(ctx, dataVolume); err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create DataVolume from snapshot: %w", err)
			}
			if err := apiReader.Get(ctx, dvKey, dataVolume); err != nil {
				return fmt.Errorf("failed to get DataVolume: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("failed to get DataVolume: %w", err)
		}

		if dataVolume.Status.Phase == cdiv1beta1.Failed {
			// Terminal: CDI will never recover a Failed DataVolume.
			return fmt.Errorf("DataVolume failed to provision (phase: Failed)")
		}
		const maxDVProvisioningTime = 2 * time.Minute
		if dataVolume.Status.Phase != cdiv1beta1.Succeeded {
			if !dataVolume.CreationTimestamp.IsZero() && time.Since(dataVolume.CreationTimestamp.Time) > maxDVProvisioningTime {
				return fmt.Errorf("DataVolume provisioning timed out after %v (phase: %s)", maxDVProvisioningTime, dataVolume.Status.Phase)
			}
			return NewTransientError(fmt.Sprintf("DataVolume is provisioning (phase: %s), will retry", dataVolume.Status.Phase))
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

	// Create volume
	volume := v1.Volume{
		Name:         volumeName,
		VolumeSource: volumeSource,
	}

	// Create disk with SCSI bus and serial number for guest detection
	disk := v1.Disk{
		Name: volumeName,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus:      v1.DiskBusSCSI,
				ReadOnly: false,
			},
		},
		Serial: serial,
	}

	// Use Patch instead of Update, with optimistic locking so a concurrent VM
	// change retries collision validation instead of overwriting the newer disk list.
	patch := client.MergeFromWithOptions(vm.DeepCopy(), client.MergeFromWithOptimisticLock{})

	// Append volume and disk to VM
	vm.Spec.Template.Spec.Volumes = append(vm.Spec.Template.Spec.Volumes, volume)
	vm.Spec.Template.Spec.Domain.Devices.Disks = append(vm.Spec.Template.Spec.Domain.Devices.Disks, disk)

	// Patch VM (safer than Update, avoids conflicts)
	if err := c.Patch(ctx, vm, patch); err != nil {
		if errors.IsConflict(err) {
			return NewTransientError(fmt.Sprintf("failed to hotplug volume to VM: %v", err))
		}
		return fmt.Errorf("failed to hotplug volume to VM: %w", err)
	}

	return nil
}

// UnplugVolume removes the restore volume from the target VM.
// It also cleans up temporary PVCs created for snapshot sources.
func UnplugVolume(ctx context.Context, c client.Client, apiReader client.Reader, vmfr *restorev1alpha1.VirtualMachineFileRestore, vm *v1.VirtualMachine) error {
	volumeName := GetVolumeName(vmfr.Name)

	// Use Patch instead of Update to avoid conflicts from concurrent VM modifications
	patch := client.MergeFrom(vm.DeepCopy())

	// Filter out the restore volume
	filteredVolumes := []v1.Volume{}
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		if vol.Name != volumeName {
			filteredVolumes = append(filteredVolumes, vol)
		}
	}
	vm.Spec.Template.Spec.Volumes = filteredVolumes

	// Filter out the restore disk
	filteredDisks := []v1.Disk{}
	for _, disk := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		if disk.Name != volumeName {
			filteredDisks = append(filteredDisks, disk)
		}
	}
	vm.Spec.Template.Spec.Domain.Devices.Disks = filteredDisks

	// Patch VM (safer than Update, avoids conflicts)
	if err := c.Patch(ctx, vm, patch); err != nil {
		return fmt.Errorf("failed to unplug volume from VM: %w", err)
	}

	// If snapshot source, delete DataVolume (which will delete the PVC)
	if vmfr.Spec.Source.Snapshot != nil {
		dataVolume := &cdiv1beta1.DataVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name:      volumeName,
				Namespace: vmfr.Namespace,
			},
		}
		dataVolumeReader := apiReader
		if dataVolumeReader == nil {
			dataVolumeReader = c
		}
		if err := dataVolumeReader.Get(ctx, client.ObjectKeyFromObject(dataVolume), dataVolume); err != nil {
			if !errors.IsNotFound(err) {
				return NewTransientError(fmt.Sprintf("get DataVolume: %v", err))
			}
			return nil
		}
		if !isRestoreDataVolume(dataVolume, vmfr) {
			return nil
		}
		preconditions := client.Preconditions{
			UID: &dataVolume.UID,
		}
		if err := c.Delete(ctx, dataVolume, preconditions); err != nil && !errors.IsNotFound(err) {
			if errors.IsConflict(err) {
				return NewTransientError(fmt.Sprintf("delete DataVolume: %v", err))
			}
			return fmt.Errorf("delete DataVolume: %w", err)
		}
	}

	return nil
}
