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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

// VirtualMachineFileRestoreReconciler reconciles a VirtualMachineFileRestore object
type VirtualMachineFileRestoreReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=virtualmachinefilerestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=virtualmachinefilerestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=virtualmachinefilerestores/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *VirtualMachineFileRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the VirtualMachineFileRestore instance
	vmFileRestore := &restorev1alpha1.VirtualMachineFileRestore{}
	if err := r.Get(ctx, req.NamespacedName, vmFileRestore); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("VirtualMachineFileRestore resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get VirtualMachineFileRestore")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling VirtualMachineFileRestore",
		"name", vmFileRestore.Name,
		"namespace", vmFileRestore.Namespace,
		"phase", vmFileRestore.Status.Phase)

	// If already completed or failed, nothing to do
	if vmFileRestore.Status.Phase == restorev1alpha1.RestorePhaseSucceeded ||
		vmFileRestore.Status.Phase == restorev1alpha1.RestorePhaseFailed {
		logger.Info("Restore already in terminal state", "phase", vmFileRestore.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Validate the restore source
	if err := r.validateRestoreSource(vmFileRestore); err != nil {
		logger.Error(err, "Invalid restore source")
		return r.updateStatus(ctx, vmFileRestore, restorev1alpha1.RestorePhaseFailed, err.Error(), 0)
	}

	// Initialize status if this is a new restore
	if vmFileRestore.Status.Phase == "" {
		logger.Info("Initializing new restore")
		now := metav1.Now()
		vmFileRestore.Status.StartTime = &now
		vmFileRestore.Status.Phase = restorev1alpha1.RestorePhaseNew
		if err := r.Status().Update(ctx, vmFileRestore); err != nil {
			logger.Error(err, "Failed to update status to New")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Move to Init if currently New
	if vmFileRestore.Status.Phase == restorev1alpha1.RestorePhaseNew {
		logger.Info("Starting restore operation")
		vmFileRestore.Status.Phase = restorev1alpha1.RestorePhaseInit
		if err := r.Status().Update(ctx, vmFileRestore); err != nil {
			logger.Error(err, "Failed to update status to Init")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Perform the actual restore operation
	// NOTE: This is a minimal implementation. A real implementation would:
	// 1. Create a restore pod to mount both source and target volumes
	// 2. Hotplug the backup volume (read-only) to the target VM
	// 3. SSH into the guest and run the file restore helper
	// 4. Handle different source types (PVC, VolumeSnapshot, Remote)
	// 5. For manual restore mode (no sourcePath), just mount and wait for user
	logger.Info("Executing file restore operation",
		"target", vmFileRestore.Spec.Target.Name,
		"sourcePath", vmFileRestore.Spec.SourcePath)

	// Simulate restore completion
	// In manual mode (no sourcePath), fileCount would be 0
	fileCount := int32(1) // Placeholder for actual restore count
	return r.updateStatus(ctx, vmFileRestore, restorev1alpha1.RestorePhaseSucceeded, "", fileCount)
}

// validateRestoreSource ensures exactly one source type is specified
func (r *VirtualMachineFileRestoreReconciler) validateRestoreSource(vmfr *restorev1alpha1.VirtualMachineFileRestore) error {
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
		return fmt.Errorf("no restore source specified")
	}
	if sourceCount > 1 {
		return fmt.Errorf("multiple restore sources specified, only one is allowed")
	}

	return nil
}

// updateStatus updates the VirtualMachineFileRestore status
func (r *VirtualMachineFileRestoreReconciler) updateStatus(
	ctx context.Context,
	vmfr *restorev1alpha1.VirtualMachineFileRestore,
	phase restorev1alpha1.RestorePhase,
	errorMsg string,
	fileCount int32,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	vmfr.Status.Phase = phase
	vmfr.Status.ErrorMessage = errorMsg
	vmfr.Status.RestoredFilesCount = fileCount

	if phase == restorev1alpha1.RestorePhaseSucceeded || phase == restorev1alpha1.RestorePhaseFailed {
		now := metav1.Now()
		vmfr.Status.CompletionTime = &now
	}

	// Update conditions
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: vmfr.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             string(phase),
		Message:            fmt.Sprintf("Restore is in %s phase", phase),
	}

	switch phase {
	case restorev1alpha1.RestorePhaseSucceeded:
		condition.Status = metav1.ConditionTrue
		condition.Message = fmt.Sprintf("Successfully restored %d file(s)", fileCount)
	case restorev1alpha1.RestorePhaseFailed:
		condition.Message = errorMsg
	}

	// Add or update the condition
	setCondition(&vmfr.Status.Conditions, condition)

	if err := r.Status().Update(ctx, vmfr); err != nil {
		logger.Error(err, "Failed to update VirtualMachineFileRestore status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// setCondition adds or updates a condition in the condition list
func setCondition(conditions *[]metav1.Condition, newCondition metav1.Condition) {
	if conditions == nil {
		return
	}

	for i, condition := range *conditions {
		if condition.Type == newCondition.Type {
			(*conditions)[i] = newCondition
			return
		}
	}
	*conditions = append(*conditions, newCondition)
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualMachineFileRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&restorev1alpha1.VirtualMachineFileRestore{}).
		Named("virtualmachinefilerestore").
		Complete(r)
}
