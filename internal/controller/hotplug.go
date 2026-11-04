package controller

import (
	"context"
	"crypto/sha256"
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

const (
	// maxSCSISerialLength is the maximum SCSI serial number length accepted by QEMU/virtio-scsi.
	// KubeVirt's admission webhook only enforces the 63-char Kubernetes label limit, so names in
	// the 37-63 char range pass admission but get silently truncated/rejected by the hypervisor.
	maxSCSISerialLength = 36

	volumeNameSuffix = "-restore"

	// shortenedHashLen is the number of SHA-256 hex digits appended when shortening is needed.
	shortenedHashLen = 8

	// shortenedPrefixLen is how many leading CR-name characters are kept for readability.
	// shortenedPrefixLen + "-" + shortenedHashLen + len(volumeNameSuffix) == maxSCSISerialLength
	shortenedPrefixLen = maxSCSISerialLength - len(volumeNameSuffix) - shortenedHashLen - 1
)

// GetVolumeName returns the volume name for a given restore CR name.
// The volume name is used as the Kubernetes volume/disk name and as the SCSI serial number
// for guest OS detection. When the name would exceed the SCSI serial limit it is automatically
// shortened to "<prefix>-<8-hex-hash>-restore" (always ≤ maxSCSISerialLength chars).
// Panics if crName is empty (caller error - should never happen with valid K8s objects).
func GetVolumeName(crName string) string {
	if crName == "" {
		panic("GetVolumeName called with empty crName")
	}
	name := crName + volumeNameSuffix
	if len(name) <= maxSCSISerialLength {
		return name
	}
	sum := sha256.Sum256([]byte(crName))
	hash := fmt.Sprintf("%x", sum[:shortenedHashLen/2]) // 4 bytes → exactly 8 hex chars
	// Trim trailing hyphens and dots from the prefix so the result is a valid DNS label
	// (no "--" in the serial, no ".-" which K8s rejects in DataVolume/PVC names).
	prefix := strings.TrimRight(crName[:shortenedPrefixLen], "-.")
	return prefix + "-" + hash + volumeNameSuffix
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

// HotplugVolume hotplugs a restore volume to the target VM.
// It handles PVC and snapshot sources, creating temporary PVCs for snapshots.
// apiReader is a non-cached reader used to read DataVolume status immediately after creation.
func HotplugVolume(ctx context.Context, c client.Client, apiReader client.Reader, vmfr *restorev1alpha1.VirtualMachineFileRestore, vm *v1.VirtualMachine) error {
	logger := log.FromContext(ctx)
	volumeName := GetVolumeName(vmfr.Name)

	// Issue #16: Check for other restore operations on this VM.
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		if strings.HasSuffix(vol.Name, volumeNameSuffix) && vol.Name != volumeName {
			return fmt.Errorf("another restore is in progress (volume %s exists), cannot hotplug", vol.Name)
		}
	}

	// Issue #15: Check both volume and disk for idempotency.
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
		// Both exist, already hotplugged.
		return nil
	}
	if volumeExists || diskExists {
		// Partial state - this shouldn't happen but handle it.
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
		if err := ensureSnapshotDataVolume(ctx, c, apiReader, vmfr, volumeName); err != nil {
			return err
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
		Serial: volumeName,
	}

	// Use Patch instead of Update to avoid conflicts from concurrent VM modifications
	patch := client.MergeFrom(vm.DeepCopy())

	// Append volume and disk to VM
	vm.Spec.Template.Spec.Volumes = append(vm.Spec.Template.Spec.Volumes, volume)
	vm.Spec.Template.Spec.Domain.Devices.Disks = append(vm.Spec.Template.Spec.Domain.Devices.Disks, disk)

	// Patch VM (safer than Update, avoids conflicts)
	if err := c.Patch(ctx, vm, patch); err != nil {
		return fmt.Errorf("failed to hotplug volume to VM: %w", err)
	}

	return nil
}

// ensureSnapshotDataVolume creates or validates the DataVolume that backs a snapshot-source
// restore. It returns nil only when the DataVolume is owned by this VMFR and has Succeeded.
// Any other outcome returns an error (possibly transient) so the caller can return early.
func ensureSnapshotDataVolume(ctx context.Context, c client.Client, apiReader client.Reader, vmfr *restorev1alpha1.VirtualMachineFileRestore, volumeName string) error {
	logger := log.FromContext(ctx)

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
		// Best-effort: failures fall back to the CDI default rather than hard-failing.
		volumeMode, modeErr := getSnapshotSourceVolumeMode(ctx, apiReader, vmfr.Spec.Source.Snapshot.Name, snapshotNamespace)
		if modeErr != nil {
			logger.Info("Could not determine source volume mode, using CDI default", "error", modeErr)
		}

		dataVolume = &cdiv1beta1.DataVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name:      volumeName,
				Namespace: vmfr.Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "vm-file-restore-operator",
				},
				// Store ownership in an annotation rather than a label: label values are
				// limited to 63 chars but CR names can be up to 253 chars.
				Annotations: map[string]string{
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
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					VolumeMode:  volumeMode,
				},
			},
		}

		// Tolerate AlreadyExists (concurrent create race); re-read to observe real status.
		if err := c.Create(ctx, dataVolume); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create DataVolume from snapshot: %w", err)
		}
		if err := apiReader.Get(ctx, dvKey, dataVolume); err != nil {
			return fmt.Errorf("failed to get DataVolume: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get DataVolume: %w", err)
	}

	// Verify ownership on ALL read paths — including the re-read after an AlreadyExists
	// create race, where a DataVolume belonging to a different restore could be returned.
	if owner := dataVolume.Annotations["filerestore.kubevirt.io/name"]; owner != vmfr.Name {
		return fmt.Errorf("DataVolume %s is owned by a different restore (%q), cannot reuse", dvKey.Name, owner)
	}

	if dataVolume.Status.Phase == cdiv1beta1.Failed {
		return fmt.Errorf("DataVolume failed to provision (phase: Failed)")
	}
	const maxDVProvisioningTime = 2 * time.Minute
	if dataVolume.Status.Phase != cdiv1beta1.Succeeded {
		if !dataVolume.CreationTimestamp.IsZero() && time.Since(dataVolume.CreationTimestamp.Time) > maxDVProvisioningTime {
			return fmt.Errorf("DataVolume provisioning timed out after %v (phase: %s)", maxDVProvisioningTime, dataVolume.Status.Phase)
		}
		return NewTransientError(fmt.Sprintf("DataVolume is provisioning (phase: %s), will retry", dataVolume.Status.Phase))
	}
	return nil
}

// UnplugVolume removes the restore volume from the target VM.
// It also cleans up temporary PVCs created for snapshot sources.
// apiReader is an uncached reader used to look up the DataVolume so a freshly created
// object is not missed due to cache lag.
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

	// If snapshot source, delete DataVolume (which will delete the PVC).
	// Ownership is verified before deletion so we never remove another CR's DataVolume.
	if vmfr.Spec.Source.Snapshot != nil {
		if err := deleteOwnedDataVolume(ctx, c, apiReader, volumeName, vmfr.Namespace, vmfr.Name); err != nil {
			return err
		}
	}

	return nil
}

// deleteOwnedDataVolume deletes the named DataVolume only when it carries the expected
// filerestore.kubevirt.io/name annotation. Returns nil if the DataVolume is not found or belongs
// to a different restore (ownership mismatch is not an error — just skip).
// apiReader is an uncached reader so a freshly created DataVolume is not missed due to
// informer cache lag. Delete uses a UID precondition so a race where the owned DV is
// deleted and immediately recreated by another restore cannot delete the replacement.
func deleteOwnedDataVolume(ctx context.Context, c client.Client, apiReader client.Reader, name, namespace, vmfrName string) error {
	dv := &cdiv1beta1.DataVolume{}
	if err := apiReader.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, dv); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get DataVolume %s: %w", name, err)
	}
	if dv.Annotations["filerestore.kubevirt.io/name"] != vmfrName {
		return nil // belongs to a different restore; do not delete
	}
	uid := dv.UID
	if err := c.Delete(ctx, dv, client.Preconditions{UID: &uid}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete DataVolume %s: %w", name, err)
	}
	return nil
}
