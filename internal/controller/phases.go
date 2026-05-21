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

// phaseHandler is a function signature for phase handlers.
type phaseHandler func(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error)

// getPhaseHandler returns the appropriate handler for a given phase.
func getPhaseHandler(phase restorev1alpha1.RestorePhase) phaseHandler {
	switch phase {
	case restorev1alpha1.RestorePhaseNew, "":
		return handleInitPhase
	case restorev1alpha1.RestorePhaseInit:
		return handleHotpluggingPhase
	case restorev1alpha1.RestorePhaseHotplugging:
		return handleWaitingForAttachmentPhase
	case restorev1alpha1.RestorePhaseWaitingForAttachment:
		return handleSSHConnectingPhase
	case restorev1alpha1.RestorePhaseSSHConnecting:
		return handleRestoringPhase
	case restorev1alpha1.RestorePhaseRestoring:
		return handlePostRestorePhase
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

	// Update status
	if err := r.Status().Update(ctx, vmfr); err != nil {
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

// failRestore transitions the restore to Failed phase with error details.
func failRestore(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore, err error, detail string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

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

	// Update status
	if updateErr := r.Status().Update(ctx, vmfr); updateErr != nil {
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

	// Detect guest OS
	osType, mountPath := DetectGuestOS(vmi)
	logger.Info("Detected guest OS", "osType", osType, "mountPath", mountPath)

	// Set mount path in status
	vmfr.Status.MountPath = mountPath
	if err := r.Status().Update(ctx, vmfr); err != nil {
		logger.Error(err, "Failed to update status with mount path")
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
	if err := HotplugVolume(ctx, r.Client, vmfr, vm); err != nil {
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
			volumeStatus = &vmi.Status.VolumeStatus[i]
			break
		}
	}

	// If volume not found in status, requeue
	if volumeStatus == nil {
		logger.Info("Volume not yet in VMI status, requeuing", "volumeName", volumeName)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// If volume not bound, requeue
	if volumeStatus.Phase != v1.VolumeReady {
		logger.Info("Volume not yet bound", "volumeName", volumeName, "phase", volumeStatus.Phase)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// If volume bound but no target, requeue
	if volumeStatus.Target == "" {
		logger.Info("Volume bound but target not set", "volumeName", volumeName)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	logger.Info("Volume attached and bound", "volumeName", volumeName, "target", volumeStatus.Target)

	// Transition to SSHConnecting
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseSSHConnecting, "Volume attached, establishing SSH connection")
}

// handleSSHConnectingPhase establishes SSH connection and determines next phase.
func handleSSHConnectingPhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
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

	// Connect SSH
	sshClient, err := ConnectSSH(ip, privateKey)
	if err != nil {
		return failRestore(ctx, r, vmfr, err, fmt.Sprintf("failed to establish SSH connection to %s", ip))
	}
	defer sshClient.Close()

	logger.Info("SSH connection established", "ip", ip)

	// Detect OS and mount path (already set in Init, but re-detect for safety)
	osType, mountPath := DetectGuestOS(vmi)
	logger.Info("Guest OS detection complete", "osType", osType, "mountPath", mountPath)

	// Determine next phase based on sourcePath
	if vmfr.Spec.SourcePath == "" {
		// Manual mode: transition to VolumeReady
		logger.Info("Manual restore mode (no sourcePath), transitioning to VolumeReady")
		return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseVolumeReady, "Volume ready for manual restore")
	}

	// Automatic mode: transition to Restoring
	logger.Info("Automatic restore mode, transitioning to Restoring")
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseRestoring, "SSH connected, starting file restore")
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
	defer sshClient.Close()

	// Build restore command
	osType, _ := DetectGuestOS(vmi)
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

	// Parse output for file count
	// Expected output format: "X files restored" or "Restored X files"
	fileCount := int32(0)
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "files restored") || strings.Contains(line, "Restored") {
			// Try to parse number from line
			var count int32
			if _, err := fmt.Sscanf(line, "%d", &count); err == nil {
				fileCount = count
				break
			}
		}
	}

	// Set restored files count
	vmfr.Status.RestoredFilesCount = fileCount
	if err := r.Status().Update(ctx, vmfr); err != nil {
		logger.Error(err, "Failed to update status with file count")
		return ctrl.Result{}, err
	}

	logger.Info("File restore completed", "filesRestored", fileCount)

	// Transition to Cleanup
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseCleanup, fmt.Sprintf("Restored %d files, cleaning up", fileCount))
}

// handlePostRestorePhase determines the next phase after restoring.
func handlePostRestorePhase(ctx context.Context, r *VirtualMachineFileRestoreReconciler, vmfr *restorev1alpha1.VirtualMachineFileRestore) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if manual mode (sourcePath empty)
	if vmfr.Spec.SourcePath == "" {
		logger.Info("Manual restore mode, transitioning to VolumeReady")
		return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseVolumeReady, "Volume ready for manual restore")
	}

	// Automatic mode: transition to Cleanup
	logger.Info("Automatic restore mode, transitioning to Cleanup")
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseCleanup, "Restore complete, cleaning up")
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
			// VM already deleted, skip cleanup
			logger.Info("Target VM not found, skipping cleanup")
			return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseSucceeded, "Restore completed (VM not found)")
		}
		return failRestore(ctx, r, vmfr, err, "failed to get target VM for cleanup")
	}

	// Unplug volume
	if err := UnplugVolume(ctx, r.Client, vmfr, vm); err != nil {
		return failRestore(ctx, r, vmfr, err, "failed to unplug volume from VM")
	}

	logger.Info("Volume unplugged from VM", "volumeName", GetVolumeName(vmfr.Name))

	// Record event
	r.Recorder.Event(vmfr, corev1.EventTypeNormal, "VolumeUnplugged", "Volume unplugged from VM")

	// Transition to Succeeded
	filesRestored := vmfr.Status.RestoredFilesCount
	return transitionPhase(ctx, r, vmfr, restorev1alpha1.RestorePhaseSucceeded, fmt.Sprintf("Restore completed successfully (%d files)", filesRestored))
}
