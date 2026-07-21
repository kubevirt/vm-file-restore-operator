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

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	v1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

// getSourceName returns the source name (PVC or snapshot name) from the VMFR spec.
// This is used to generate unique mount paths.
func getSourceName(vmfr *restorev1alpha1.VirtualMachineFileRestore) string {
	if vmfr.Spec.Source.PVC != nil {
		return vmfr.Spec.Source.PVC.Name
	}
	if vmfr.Spec.Source.Snapshot != nil {
		return vmfr.Spec.Source.Snapshot.Name
	}
	// Fallback to VMFR name if no source specified (shouldn't happen with validation)
	return vmfr.Name
}

// phaseHandler is a function signature for phase handlers.
type phaseHandler func(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error)

// getPhaseHandler returns the appropriate handler for a given phase.
func getPhaseHandler(phase restorev1alpha1.RestorePhase) phaseHandler {
	switch phase {
	case restorev1alpha1.RestorePhaseNew, "":
		return handleInitPhase
	case restorev1alpha1.RestorePhaseInit:
		return handleInitPhase
	case restorev1alpha1.RestorePhaseHotplugging:
		return handleHotpluggingPhase
	case restorev1alpha1.RestorePhaseWaitingForAttachment:
		return handleWaitingForAttachmentPhase
	case restorev1alpha1.RestorePhaseSSHConnecting:
		return handleSSHConnectingPhase
	case restorev1alpha1.RestorePhaseRestoring:
		return handleRestoringPhase
	case restorev1alpha1.RestorePhaseVolumeReady:
		return handleVolumeReadyPhase
	case restorev1alpha1.RestorePhaseCleanup:
		return handleCleanupPhase
	default:
		return nil
	}
}

// transitionPhase transitions the restore to a new phase.
func transitionPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore, newPhase restorev1alpha1.RestorePhase, message string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	oldPhase := vmfr.Status.Phase

	// Create a copy for status update to avoid modifying in-memory object on failure
	patch := client.MergeFrom(vmfr.DeepCopy())

	vmfr.Status.Phase = newPhase

	// Set StartTime if transitioning from New
	if oldPhase == "" || oldPhase == restorev1alpha1.RestorePhaseNew {
		now := metav1.Now()
		vmfr.Status.StartTime = &now
	}

	// Set CompletionTime if terminal phase
	if newPhase == restorev1alpha1.RestorePhaseSucceeded || newPhase == restorev1alpha1.RestorePhaseFailed {
		now := metav1.Now()
		vmfr.Status.CompletionTime = &now
	}

	// Use Status().Patch instead of Update for better conflict handling
	if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
		logger.Error(err, "Failed to update status during phase transition",
			"oldPhase", oldPhase,
			"newPhase", newPhase)
		return ctrl.Result{}, err
	}

	// Log transition
	logger.Info("Phase transition",
		"oldPhase", oldPhase,
		"newPhase", newPhase,
		"message", message)

	// Record event
	eventType := corev1.EventTypeNormal
	if newPhase == restorev1alpha1.RestorePhaseFailed {
		eventType = corev1.EventTypeWarning
	}
	r.Recorder.Event(vmfr, eventType, string(newPhase), message)

	return ctrl.Result{Requeue: true}, nil
}

// incrementRetryAndRequeue increments a retry counter and requeues with exponential backoff.
// retryType should be "attachment" or "ssh".
//
//nolint:unparam // baseDelay is kept as parameter for future flexibility
func incrementRetryAndRequeue(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore, retryType string, baseDelay time.Duration) (ctrl.Result, error) {
	patch := client.MergeFrom(vmfr.DeepCopy())

	switch retryType {
	case "attachment":
		vmfr.Status.AttachmentRetries++
	case "ssh":
		vmfr.Status.SSHRetries++
	}

	if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to increment %s retry counter: %w", retryType, err)
	}

	// Use exponential backoff with cap (issue #21)
	delay := baseDelay
	var retries int32
	if retryType == "attachment" {
		retries = vmfr.Status.AttachmentRetries
	} else {
		retries = vmfr.Status.SSHRetries
	}

	// Double delay every 5 retries, capped at 30 seconds
	if retries >= 5 && retries < 10 {
		delay = baseDelay * 2
	} else if retries >= 10 {
		delay = baseDelay * 4
	}
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}

	return ctrl.Result{RequeueAfter: delay}, nil
}

// failRestore transitions the restore to Failed phase with error details.
func failRestore(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore, err error, detail string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Create a copy for status update to avoid modifying in-memory object on failure
	patch := client.MergeFrom(vmfr.DeepCopy())

	// Set Phase to Failed
	vmfr.Status.Phase = restorev1alpha1.RestorePhaseFailed

	// Set ErrorMessage
	vmfr.Status.ErrorMessage = err.Error()

	// Set CompletionTime
	now := metav1.Now()
	vmfr.Status.CompletionTime = &now

	// Add condition
	condition := metav1.Condition{
		Type:               "RestoreCompleted",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: vmfr.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             "RestoreFailed",
		Message:            TruncateOutput(detail, 100),
	}
	setCondition(&vmfr.Status.Conditions, condition)

	// Use Status().Patch instead of Update for better conflict handling
	if updateErr := r.Status().Patch(ctx, vmfr, patch); updateErr != nil {
		logger.Error(updateErr, "Failed to update status during failure handling")
		return ctrl.Result{}, updateErr
	}

	// Log error
	logger.Error(err, "Restore failed", "detail", detail)

	// Record warning event
	r.Recorder.Event(vmfr, corev1.EventTypeWarning, "RestoreFailed", fmt.Sprintf("%s: %s", err.Error(), TruncateOutput(detail, 100)))

	// Best-effort cleanup
	if cleanupErr := r.cleanup(ctx, vmfr); cleanupErr != nil {
		logger.Error(cleanupErr, "Cleanup failed during failure handling, ignoring")
	}

	return ctrl.Result{}, nil
}

// handleInitPhase validates the target VM and source, detects OS, and transitions to Hotplugging.
func handleInitPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Validate exactly one source is specified (issue #3)
	sourceCount := 0
	if vmfr.Spec.Source.PVC != nil {
		sourceCount++
	}
	if vmfr.Spec.Source.Snapshot != nil {
		sourceCount++
	}
	if vmfr.Spec.Source.Remote != nil {
		sourceCount++
	}
	if sourceCount == 0 {
		return failRestore(ctx, r, vmfr, fmt.Errorf("no source specified"), "must specify exactly one of pvc, snapshot, or remote")
	}
	if sourceCount > 1 {
		return failRestore(ctx, r, vmfr, fmt.Errorf("multiple sources specified"), "must specify exactly one of pvc, snapshot, or remote")
	}

	// Get target VM
	vm := &v1.VirtualMachine{}
	vmKey := client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}
	if err := r.Get(ctx, vmKey, vm); err != nil {
		if errors.IsNotFound(err) {
			return failRestore(ctx, r, vmfr, err, fmt.Sprintf("target VM %s not found", vmfr.Spec.Target.Name))
		}
		return failRestore(ctx, r, vmfr, err, "failed to get target VM")
	}

	// Get VMI to ensure VM is running
	vmi := &v1.VirtualMachineInstance{}
	vmiKey := client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}
	if err := r.Get(ctx, vmiKey, vmi); err != nil {
		if errors.IsNotFound(err) {
			return failRestore(ctx, r, vmfr, err, fmt.Sprintf("target VM %s is not running (no VMI found)", vmfr.Spec.Target.Name))
		}
		return failRestore(ctx, r, vmfr, err, "failed to get VMI")
	}

	// Validate source exists
	if vmfr.Spec.Source.PVC != nil {
		pvc := &corev1.PersistentVolumeClaim{}
		pvcNamespace := vmfr.Spec.Source.PVC.Namespace
		if pvcNamespace == "" {
			pvcNamespace = vmfr.Namespace
			logger.Info("PVC namespace not specified, using CR namespace", "namespace", pvcNamespace)
		}
		pvcKey := client.ObjectKey{
			Name:      vmfr.Spec.Source.PVC.Name,
			Namespace: pvcNamespace,
		}
		if err := r.Get(ctx, pvcKey, pvc); err != nil {
			if errors.IsNotFound(err) {
				return failRestore(ctx, r, vmfr, err, fmt.Sprintf("source PVC %s/%s not found", pvcNamespace, vmfr.Spec.Source.PVC.Name))
			}
			return failRestore(ctx, r, vmfr, err, "failed to get source PVC")
		}
	} else if vmfr.Spec.Source.Snapshot != nil {
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
		if err := r.Get(ctx, snapshotKey, snapshot); err != nil {
			if errors.IsNotFound(err) {
				return failRestore(ctx, r, vmfr, err, fmt.Sprintf("source snapshot %s/%s not found", snapshotNamespace, vmfr.Spec.Source.Snapshot.Name))
			}
			return failRestore(ctx, r, vmfr, err, "failed to get source snapshot")
		}
	} else if vmfr.Spec.Source.Remote != nil {
		return failRestore(ctx, r, vmfr, fmt.Errorf("remote sources not supported"), "remote restore source is not yet implemented")
	}

	// Detect guest OS and generate mount path based on source name
	osType := DetectGuestOS(vmi)
	sourceName := getSourceName(vmfr)
	mountPath := getMountPath(vmi, sourceName)
	logger.Info("Detected guest OS", "osType", osType, "mountPath", mountPath, "sourceName", sourceName)

	// Set mount path in status using Patch for consistency (issue #1)
	patch := client.MergeFrom(vmfr.DeepCopy())
	vmfr.Status.MountPath = mountPath
	if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
		logger.Error(err, "Failed to update status with mount path")
		// Reset in-memory state to match persisted state on failure
		vmfr.Status.MountPath = ""
		return ctrl.Result{}, err
	}

	// Transition to Hotplugging phase
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseHotplugging, "Validation complete, starting volume hotplug")
}

// handleHotpluggingPhase hotplugs the restore volume to the VM and transitions to WaitingForAttachment.
func handleHotpluggingPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get target VM
	vm := &v1.VirtualMachine{}
	vmKey := client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}
	if err := r.Get(ctx, vmKey, vm); err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to get target VM for hotplug")
	}

	// Hotplug the volume
	if err := HotplugVolume(ctx, r.Client, r.APIReader, vmfr, vm); err != nil {
		// Issue #5: Handle transient errors by requeuing instead of failing
		if IsTransient(err) {
			logger.Info("Hotplug encountered transient condition, will retry", "error", err)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return failRestore(ctx, r, vmfr, err, "failed to hotplug volume to VM")
	}

	logger.Info("Volume hotplugged to VM", "volumeName", GetVolumeName(vmfr.Name))

	// Record event
	r.Recorder.Event(vmfr, corev1.EventTypeNormal, "VolumeHotplug", "Volume hotplugged to VM")

	// Transition to WaitingForAttachment
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseWaitingForAttachment, "Volume hotplug initiated, waiting for attachment")
}

// handleWaitingForAttachmentPhase waits for the volume to be attached and bound to the VMI.
func handleWaitingForAttachmentPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check timeout (issue #6): max 5 minutes (60 retries * 5 seconds)
	const maxAttachmentWait = 60
	if vmfr.Status.AttachmentRetries >= maxAttachmentWait {
		return failRestore(ctx, r, vmfr,
			fmt.Errorf("volume attachment timeout"),
			fmt.Sprintf("volume did not attach after %d attempts (5 minutes)", maxAttachmentWait))
	}

	// Rate limiting: ensure at least 5 seconds between attachment checks
	// to prevent rapid reconciliation loops from external triggers (VMI updates, etc.)
	if vmfr.Status.LastAttachmentCheckTime != nil {
		timeSinceLastCheck := time.Since(vmfr.Status.LastAttachmentCheckTime.Time)
		if timeSinceLastCheck < 5*time.Second {
			remainingWait := 5*time.Second - timeSinceLastCheck
			logger.Info("Rate limiting attachment check", "remainingWait", remainingWait)
			return ctrl.Result{RequeueAfter: remainingWait}, nil
		}
	}

	// Get VMI
	vmi := &v1.VirtualMachineInstance{}
	vmiKey := client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}
	if err := r.Get(ctx, vmiKey, vmi); err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to get VMI")
	}

	// Check volume status
	volumeName := GetVolumeName(vmfr.Name)
	var volumeStatus *v1.VolumeStatus
	for i := range vmi.Status.VolumeStatus {
		if vmi.Status.VolumeStatus[i].Name == volumeName {
			// Copy value instead of taking pointer to slice element to avoid aliasing issues
			vs := vmi.Status.VolumeStatus[i]
			volumeStatus = &vs
			break
		}
	}

	// Update last check timestamp before checking status
	now := metav1.Now()
	patch := client.MergeFrom(vmfr.DeepCopy())
	vmfr.Status.LastAttachmentCheckTime = &now
	if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
		logger.Error(err, "Failed to update last attachment check time")
		return ctrl.Result{}, err
	}

	// If volume not found in status, increment retry and requeue
	if volumeStatus == nil {
		logger.Info("Volume not yet in VMI status, requeuing", "volumeName", volumeName, "attempt", vmfr.Status.AttachmentRetries+1)
		return incrementRetryAndRequeue(ctx, r, vmfr, "attachment", 5*time.Second)
	}

	// If volume not bound, increment retry and requeue
	if volumeStatus.Phase != v1.VolumeReady {
		logger.Info("Volume not yet bound", "volumeName", volumeName, "phase", volumeStatus.Phase, "attempt", vmfr.Status.AttachmentRetries+1)
		return incrementRetryAndRequeue(ctx, r, vmfr, "attachment", 5*time.Second)
	}

	// If volume bound but no target, increment retry and requeue
	if volumeStatus.Target == "" {
		logger.Info("Volume bound but target not set", "volumeName", volumeName, "attempt", vmfr.Status.AttachmentRetries+1)
		return incrementRetryAndRequeue(ctx, r, vmfr, "attachment", 5*time.Second)
	}

	// Verify it's actually a hotplug volume
	if volumeStatus.HotplugVolume == nil {
		return failRestore(ctx, r, vmfr, fmt.Errorf("volume is not a hotplug volume"),
			fmt.Sprintf("Volume %s exists but is not marked as hotplugged", volumeName))
	}

	logger.Info("Volume attached and bound", "volumeName", volumeName, "target", volumeStatus.Target)

	// Reset retry counter
	if vmfr.Status.AttachmentRetries > 0 {
		patch := client.MergeFrom(vmfr.DeepCopy())
		vmfr.Status.AttachmentRetries = 0
		if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
			logger.Error(err, "Failed to reset attachment retry counter")
		}
	}

	// Transition to SSHConnecting
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseSSHConnecting, "Volume attached, establishing SSH connection")
}

// handleSSHConnectingPhase establishes SSH connection and determines next phase.
func handleSSHConnectingPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check timeout (issue #7): max 2 minutes (24 retries * 5 seconds)
	const maxSSHWait = 24
	if vmfr.Status.SSHRetries >= maxSSHWait {
		return failRestore(ctx, r, vmfr,
			fmt.Errorf("SSH connection timeout"),
			fmt.Sprintf("SSH connection failed after %d attempts (2 minutes)", maxSSHWait))
	}

	// Rate limiting: ensure at least 5 seconds between SSH connection attempts
	// to prevent rapid reconciliation loops from external triggers
	if vmfr.Status.LastSSHCheckTime != nil {
		timeSinceLastCheck := time.Since(vmfr.Status.LastSSHCheckTime.Time)
		if timeSinceLastCheck < 5*time.Second {
			remainingWait := 5*time.Second - timeSinceLastCheck
			logger.Info("Rate limiting SSH check", "remainingWait", remainingWait)
			return ctrl.Result{RequeueAfter: remainingWait}, nil
		}
	}

	// Update last check timestamp before attempting SSH connection
	now := metav1.Now()
	patch := client.MergeFrom(vmfr.DeepCopy())
	vmfr.Status.LastSSHCheckTime = &now
	if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
		logger.Error(err, "Failed to update last SSH check time")
		return ctrl.Result{}, err
	}

	// Get VMI
	vmi := &v1.VirtualMachineInstance{}
	vmiKey := client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}
	if err := r.Get(ctx, vmiKey, vmi); err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to get VMI")
	}

	// Get IP address (issue #18: log which IP is selected)
	ip, err := GetVMIPAddress(ctx, r.Client, vmi)
	if err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to get VM IP address")
	}

	logger.Info("Got VM IP address", "ip", ip)

	// Get SSH private key from Secret
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Name:      SSHKeypairSecretName,
		Namespace: r.getOperatorNamespace(),
	}
	if err := r.Get(ctx, secretKey, secret); err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to get SSH keypair secret")
	}

	privateKey, ok := secret.Data[corev1.SSHAuthPrivateKey]
	if !ok {
		return failRestore(ctx, r, vmfr, fmt.Errorf("private key not found in secret"), "SSH private key missing from secret")
	}

	// Connect SSH with retry (issue #7)
	sshClient, err := ConnectSSH(ip, privateKey)
	if err != nil {
		logger.Info("SSH connection failed, will retry", "ip", ip, "error", err, "attempt", vmfr.Status.SSHRetries+1)
		return incrementRetryAndRequeue(ctx, r, vmfr, "ssh", 5*time.Second)
	}
	defer sshClient.Close() //nolint:errcheck // Closing in defer is idiomatic

	logger.Info("SSH connection established", "ip", ip)

	// Reset SSH retry counter
	if vmfr.Status.SSHRetries > 0 {
		patch := client.MergeFrom(vmfr.DeepCopy())
		vmfr.Status.SSHRetries = 0
		if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
			logger.Error(err, "Failed to reset SSH retry counter")
		}
	}

	// Detect OS and mount path (already set in Init, but re-detect for safety)
	osType := DetectGuestOS(vmi)
	sourceName := getSourceName(vmfr)
	mountPath := getMountPath(vmi, sourceName)
	logger.Info("Guest OS detection complete", "osType", osType, "mountPath", mountPath, "sourceName", sourceName)

	// All modes transition to Restoring to mount the volume
	// Manual mode (no sourcePath): script mounts read-only and exits
	// Automatic mode (with sourcePath): script mounts, restores files, unmounts
	if vmfr.Spec.SourcePath == "" {
		logger.Info("Manual restore mode (no sourcePath), transitioning to Restoring for mount-only")
	} else {
		logger.Info("Automatic restore mode, transitioning to Restoring")
	}
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseRestoring, "SSH connected, starting restore")
}

// handleRestoringPhase executes the restore command via SSH and transitions to Cleanup.
func handleRestoringPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get VMI
	vmi := &v1.VirtualMachineInstance{}
	vmiKey := client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}
	if err := r.Get(ctx, vmiKey, vmi); err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to get VMI")
	}

	// Get IP address
	ip, err := GetVMIPAddress(ctx, r.Client, vmi)
	if err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to get VM IP address")
	}

	// Get SSH private key
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Name:      SSHKeypairSecretName,
		Namespace: r.getOperatorNamespace(),
	}
	if err := r.Get(ctx, secretKey, secret); err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to get SSH keypair secret")
	}

	privateKey, ok := secret.Data[corev1.SSHAuthPrivateKey]
	if !ok {
		return failRestore(ctx, r, vmfr, fmt.Errorf("private key not found in secret"), "SSH private key missing from secret")
	}

	// Connect SSH
	sshClient, err := ConnectSSH(ip, privateKey)
	if err != nil {
		return failRestore(ctx, r, vmfr, err, fmt.Sprintf("failed to establish SSH connection to %s", ip))
	}
	defer sshClient.Close() //nolint:errcheck // Closing in defer is idiomatic

	// Build restore command
	osType := DetectGuestOS(vmi)
	volumeName := GetVolumeName(vmfr.Name)
	command := BuildSSHCommand(osType, volumeName, vmfr.Status.MountPath, vmfr.Spec.SourcePath)

	logger.Info("Executing restore command", "command", command)

	// Run command
	stdout, stderr, err := sshClient.RunCommand(ctx, command)
	if err != nil {
		detail := fmt.Sprintf("restore command failed: %s\nstdout: %s\nstderr: %s", err.Error(), TruncateOutput(stdout, 50), TruncateOutput(stderr, 50))
		return failRestore(ctx, r, vmfr, err, detail)
	}

	logger.Info("Restore command completed", "stdout", stdout, "stderr", stderr)

	fileCount := ParseRestoredFileCount(stdout)
	if fileCount == 0 && strings.TrimSpace(stdout) != "" {
		logger.Info("WARNING: parsed 0 files but stdout was non-empty, the guest helper may not have emitted a count line")
	}

	logger.Info("File restore completed", "filesRestored", fileCount)

	// Determine next phase based on mode
	var nextPhase restorev1alpha1.RestorePhase
	var eventMsg string

	if vmfr.Spec.SourcePath == "" {
		// Manual mode: volume is mounted, transition to VolumeReady
		nextPhase = restorev1alpha1.RestorePhaseVolumeReady
		eventMsg = "Volume mounted at " + vmfr.Status.MountPath + ", ready for manual restore"
		logger.Info("Manual mode: transitioning to VolumeReady")
	} else {
		// Automatic mode: files restored, transition to Cleanup
		nextPhase = restorev1alpha1.RestorePhaseCleanup
		eventMsg = fmt.Sprintf("Restored %d files, cleaning up", fileCount)
		logger.Info("Automatic mode: transitioning to Cleanup", "filesRestored", fileCount)
	}

	// Update file count and transition phase atomically (issue #9)
	patch := client.MergeFrom(vmfr.DeepCopy())
	vmfr.Status.RestoredFilesCount = &fileCount
	vmfr.Status.Phase = nextPhase

	if err := r.Status().Patch(ctx, vmfr, patch); err != nil {
		logger.Error(err, "Failed to update status during phase transition", "targetPhase", nextPhase)
		return ctrl.Result{}, err
	}

	// Log transition and emit event
	r.Recorder.Event(vmfr, corev1.EventTypeNormal, string(nextPhase), eventMsg)
	logger.Info("Phase transition", "oldPhase", restorev1alpha1.RestorePhaseRestoring,
		"newPhase", nextPhase, "filesRestored", fileCount)

	return ctrl.Result{Requeue: true}, nil
}

// handleVolumeReadyPhase handles manual restore mode - volume is mounted, waiting for user.
func handleVolumeReadyPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Manual mode: volume is hotplugged, do nothing, wait for user
	logger.Info("Manual restore mode: volume is ready, waiting for user to complete restore")

	// Do not requeue - user will delete CR when done
	return ctrl.Result{}, nil
}

// handleCleanupPhase unplugs the volume and transitions to Succeeded.
func handleCleanupPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get target VM
	vm := &v1.VirtualMachine{}
	vmKey := client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}
	if err := r.Get(ctx, vmKey, vm); err != nil {
		if errors.IsNotFound(err) {
			// VM deleted during restore (issue #10)
			// Check if we successfully restored files before VM was deleted
			if vmfr.Status.RestoredFilesCount != nil && *vmfr.Status.RestoredFilesCount > 0 {
				logger.Info("Target VM was deleted after restore completed", "filesRestored", *vmfr.Status.RestoredFilesCount)
				return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseSucceeded,
					fmt.Sprintf("Restored %d files (VM was deleted during cleanup)", *vmfr.Status.RestoredFilesCount))
			}
			// VM deleted before restore completed
			return failRestore(ctx, r, vmfr, err, "target VM was deleted before restore could complete")
		}
		return failRestore(ctx, r, vmfr, err, "failed to get target VM for cleanup")
	}

	// Unplug volume
	if err := UnplugVolume(ctx, r.Client, vmfr, vm); err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to unplug volume from VM")
	}

	volumeName := GetVolumeName(vmfr.Name)
	logger.Info("Volume unplugged from VM spec", "volumeName", volumeName)

	// Issue #17: Verify volume is removed from VMI before completing
	vmi := &v1.VirtualMachineInstance{}
	vmiKey := client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}
	if err := r.Get(ctx, vmiKey, vmi); err != nil {
		if !errors.IsNotFound(err) {
			return failRestore(ctx, r, vmfr, err, "failed to get VMI for unplug verification")
		}
		// VMI gone, volume is definitely detached
	} else {
		// Check if volume still exists in VMI status
		for _, volumeStatus := range vmi.Status.VolumeStatus {
			if volumeStatus.Name == volumeName {
				logger.Info("Volume still in VMI status, waiting for detachment", "volumeName", volumeName)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}
	}

	logger.Info("Volume successfully detached from VM", "volumeName", volumeName)

	// Record event
	r.Recorder.Event(vmfr, corev1.EventTypeNormal, "VolumeUnplugged", "Volume unplugged from VM")

	// Transition to Succeeded
	var filesRestored int32
	if vmfr.Status.RestoredFilesCount != nil {
		filesRestored = *vmfr.Status.RestoredFilesCount
	}
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseSucceeded, fmt.Sprintf("Restore completed successfully (%d files)", filesRestored))
}

// ParseRestoredFileCount extracts a file count from guest helper stdout.
// Only lines carrying the "[filerestore] " prefix are considered, avoiding
// false positives from rsync verbose output (e.g. filenames starting with digits).
func ParseRestoredFileCount(stdout string) int32 {
	const prefix = "[filerestore] "
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		line = strings.TrimPrefix(line, prefix)

		var count int32
		if n, _ := fmt.Sscanf(line, "%d files restored", &count); n == 1 {
			return count
		}
		if n, _ := fmt.Sscanf(line, "Restored %d files", &count); n == 1 {
			return count
		}
		if n, _ := fmt.Sscanf(line, "%d files", &count); n == 1 {
			return count
		}
	}
	return 0
}
