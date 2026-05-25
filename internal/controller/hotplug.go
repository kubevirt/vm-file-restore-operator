package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	v1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

// GetVolumeName returns the volume name for a given restore CR name.
// The volume name is used as the volume name, disk name, and serial number for guest OS detection.
// Panics if crName is empty (caller error - should never happen with valid K8s objects).
func GetVolumeName(crName string) string {
	if crName == "" {
		panic("GetVolumeName called with empty crName")
	}
	return crName + "-restore"
}

// HotplugVolume hotplugs a restore volume to the target VM.
// It handles PVC and snapshot sources, creating temporary PVCs for snapshots.
func HotplugVolume(ctx context.Context, c client.Client, vmfr *restorev1alpha1.VirtualMachineFileRestore, vm *v1.VirtualMachine) error {
	logger := log.FromContext(ctx)
	volumeName := GetVolumeName(vmfr.Name)

	// Issue #16: Check for other restore operations on this VM
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		if strings.HasSuffix(vol.Name, "-restore") && vol.Name != volumeName {
			return fmt.Errorf("another restore is in progress (volume %s exists), cannot hotplug", vol.Name)
		}
	}

	// Issue #15: Check both volume and disk for idempotency
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
		// Both exist, already hotplugged
		return nil
	}
	if volumeExists || diskExists {
		// Partial state - this shouldn't happen but handle it
		logger.Error(fmt.Errorf("partial hotplug detected"), "Inconsistent state",
			"volumeExists", volumeExists, "diskExists", diskExists)
		return fmt.Errorf("partial hotplug detected (volume=%v, disk=%v), needs cleanup",
			volumeExists, diskExists)
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
		// Issue #4: Query snapshot to get size
		snapshot := &unstructured.Unstructured{}
		snapshot.SetAPIVersion("snapshot.storage.k8s.io/v1")
		snapshot.SetKind("VolumeSnapshot")
		snapshotNamespace := vmfr.Spec.Source.Snapshot.Namespace
		if snapshotNamespace == "" {
			snapshotNamespace = vmfr.Namespace
		}
		snapshotKey := client.ObjectKey{
			Name:      vmfr.Spec.Source.Snapshot.Name,
			Namespace: snapshotNamespace,
		}
		if err := c.Get(ctx, snapshotKey, snapshot); err != nil {
			return fmt.Errorf("failed to get snapshot: %w", err)
		}

		// Get restore size from snapshot status
		storageSize := resource.MustParse("10Gi") // Default fallback
		if restoreSize, found, err := unstructured.NestedString(snapshot.Object, "status", "restoreSize"); found && err == nil && restoreSize != "" {
			if parsed, err := resource.ParseQuantity(restoreSize); err == nil {
				storageSize = parsed
				logger.Info("Using snapshot restore size", "size", restoreSize)
			}
		} else {
			logger.Info("Snapshot restore size not available, using default", "size", storageSize.String())
		}

		// Snapshot source: create temporary PVC from snapshot
		tempPVC := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      volumeName,
				Namespace: vmfr.Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "vm-file-restore-operator",
					"filerestore.kubevirt.io/name": vmfr.Name,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteMany,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageSize,
					},
				},
				DataSource: &corev1.TypedLocalObjectReference{
					APIGroup: ptr.To("snapshot.storage.k8s.io"),
					Kind:     "VolumeSnapshot",
					Name:     vmfr.Spec.Source.Snapshot.Name,
				},
			},
		}

		// Create PVC, verify if already exists
		if err := c.Create(ctx, tempPVC); err != nil {
			if !errors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create temp PVC from snapshot: %w", err)
			}
		}

		// Always verify the PVC is bound before proceeding (issue #5)
		existing := &corev1.PersistentVolumeClaim{}
		if err := c.Get(ctx, client.ObjectKey{Name: volumeName, Namespace: vmfr.Namespace}, existing); err != nil {
			return fmt.Errorf("failed to get temp PVC: %w", err)
		}
		if existing.Status.Phase != corev1.ClaimBound {
			// This is a transient condition - caller will retry
			return NewTransientError(fmt.Sprintf("temp PVC is being provisioned (phase: %s), will retry", existing.Status.Phase))
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

	// Create disk with SCSI bus, read-only, and serial number for guest detection
	disk := v1.Disk{
		Name: volumeName,
		DiskDevice: v1.DiskDevice{
			Disk: &v1.DiskTarget{
				Bus:      v1.DiskBusSCSI,
				ReadOnly: true,
			},
		},
		Serial: volumeName,
	}

	// Append volume and disk to VM
	vm.Spec.Template.Spec.Volumes = append(vm.Spec.Template.Spec.Volumes, volume)
	vm.Spec.Template.Spec.Domain.Devices.Disks = append(vm.Spec.Template.Spec.Domain.Devices.Disks, disk)

	// Update VM
	if err := c.Update(ctx, vm); err != nil {
		return fmt.Errorf("failed to hotplug volume to VM: %w", err)
	}

	return nil
}

// UnplugVolume removes the restore volume from the target VM.
// It also cleans up temporary PVCs created for snapshot sources.
func UnplugVolume(ctx context.Context, c client.Client, vmfr *restorev1alpha1.VirtualMachineFileRestore, vm *v1.VirtualMachine) error {
	volumeName := GetVolumeName(vmfr.Name)

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

	// Update VM
	if err := c.Update(ctx, vm); err != nil {
		return fmt.Errorf("failed to unplug volume from VM: %w", err)
	}

	// If snapshot source, delete temp PVC
	if vmfr.Spec.Source.Snapshot != nil {
		tempPVC := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      volumeName,
				Namespace: vmfr.Namespace,
			},
		}
		if err := c.Delete(ctx, tempPVC); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete temp PVC: %w", err)
		}
	}

	return nil
}
