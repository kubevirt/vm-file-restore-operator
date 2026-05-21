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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	v1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

const (
	finalizerName = "filerestore.kubevirt.io/cleanup"
)

// VirtualMachineFileRestoreReconciler reconciles a VirtualMachineFileRestore object
type VirtualMachineFileRestoreReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	Recorder          record.EventRecorder
	OperatorNamespace string
}

// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=virtualmachinefilerestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=virtualmachinefilerestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=virtualmachinefilerestores/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create
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

	// Handle deletion
	if !vmFileRestore.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(vmFileRestore, finalizerName) {
			logger.Info("VirtualMachineFileRestore is being deleted, running cleanup")

			// Run cleanup (best-effort, continue to remove finalizer even on error)
			if err := r.cleanup(ctx, vmFileRestore); err != nil {
				logger.Error(err, "Error during cleanup, continuing to remove finalizer")
			}

			// Remove finalizer
			controllerutil.RemoveFinalizer(vmFileRestore, finalizerName)
			if err := r.Update(ctx, vmFileRestore); err != nil {
				logger.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}
			logger.Info("Finalizer removed, deletion will proceed")
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(vmFileRestore, finalizerName) {
		logger.Info("Adding finalizer")
		controllerutil.AddFinalizer(vmFileRestore, finalizerName)
		if err := r.Update(ctx, vmFileRestore); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// If already completed or failed, nothing to do
	if vmFileRestore.Status.Phase == restorev1alpha1.RestorePhaseSucceeded ||
		vmFileRestore.Status.Phase == restorev1alpha1.RestorePhaseFailed {
		logger.Info("Restore already in terminal state", "phase", vmFileRestore.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Run phase handler
	handler := getPhaseHandler(vmFileRestore.Status.Phase)
	if handler == nil {
		logger.Error(fmt.Errorf("unknown phase"), "No handler for phase", "phase", vmFileRestore.Status.Phase)
		return ctrl.Result{}, nil
	}

	return handler(ctx, r, vmFileRestore)
}

// cleanup performs cleanup when CR is deleted
func (r *VirtualMachineFileRestoreReconciler) cleanup(ctx context.Context, vmfr *restorev1alpha1.VirtualMachineFileRestore) error {
	logger := log.FromContext(ctx)
	logger.Info("Running cleanup", "name", vmfr.Name)

	// Best effort cleanup - don't fail on errors

	// Get VMI if exists
	vmi := &v1.VirtualMachineInstance{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vmi)

	if err == nil {
		// Run SSH cleanup
		osType, _ := DetectGuestOS(vmi)
		ip, err := GetVMIPAddress(ctx, r.Client, vmi)
		if err == nil {
			operatorNamespace := r.getOperatorNamespace()
			secret := &corev1.Secret{}
			err = r.Get(ctx, client.ObjectKey{
				Name:      SSHKeypairSecretName,
				Namespace: operatorNamespace,
			}, secret)
			if err == nil {
				privateKey := secret.Data[corev1.SSHAuthPrivateKey]
				sshClient, err := ConnectSSH(ip, privateKey)
				if err == nil {
					defer sshClient.Close()
					cleanupCmd := BuildCleanupCommand(osType, vmfr.Status.MountPath)
					_, _, _ = sshClient.RunCommand(ctx, cleanupCmd)
				}
			}
		}
	}

	// Unplug volume
	vm := &v1.VirtualMachine{}
	err = r.Get(ctx, client.ObjectKey{
		Name:      vmfr.Spec.Target.Name,
		Namespace: vmfr.Namespace,
	}, vm)
	if err == nil {
		_ = UnplugVolume(ctx, r.Client, vmfr, vm)
	}

	return nil
}

// getOperatorNamespace returns the namespace where the operator is running.
func (r *VirtualMachineFileRestoreReconciler) getOperatorNamespace() string {
	if r.OperatorNamespace != "" {
		return r.OperatorNamespace
	}
	return "vm-file-restore-operator-system"
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
	r.Recorder = mgr.GetEventRecorderFor("virtualmachinefilerestore-controller")

	return ctrl.NewControllerManagedBy(mgr).
		For(&restorev1alpha1.VirtualMachineFileRestore{}).
		Named("virtualmachinefilerestore").
		Complete(r)
}
