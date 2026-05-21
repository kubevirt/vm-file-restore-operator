package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	v1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

// GetVolumeName returns the volume name for a given restore CR name.
// The volume name is used as the volume name, disk name, and serial number for guest OS detection.
func GetVolumeName(crName string) string {
	return crName + "-restore"
}

// HotplugVolume hotplugs a restore volume to the target VM.
// It handles PVC and snapshot sources, creating temporary PVCs for snapshots.
func HotplugVolume(ctx context.Context, c client.Client, vmfr *restorev1alpha1.VirtualMachineFileRestore, vm *v1.VirtualMachine) error {
	volumeName := GetVolumeName(vmfr.Name)

	// Check if volume already exists
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		if vol.Name == volumeName {
			// Volume already hotplugged, nothing to do
			return nil
		}
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
		// Snapshot source: create temporary PVC from snapshot first
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
					corev1.ReadOnlyMany,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"), // TODO: match snapshot size
					},
				},
				DataSource: &corev1.TypedLocalObjectReference{
					APIGroup: ptr.To("snapshot.storage.k8s.io"),
					Kind:     "VolumeSnapshot",
					Name:     vmfr.Spec.Source.Snapshot.Name,
				},
			},
		}

		// Create PVC, ignore if already exists (idempotency)
		if err := c.Create(ctx, tempPVC); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create temp PVC from snapshot: %w", err)
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
