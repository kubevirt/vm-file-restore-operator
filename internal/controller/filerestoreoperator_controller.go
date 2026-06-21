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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	sdkapi "kubevirt.io/controller-lifecycle-operator-sdk/api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

// FileRestoreOperatorReconciler reconciles a FileRestoreOperator object
type FileRestoreOperatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=filerestoreoperators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=filerestoreoperators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=filerestoreoperators/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop
func (r *FileRestoreOperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the FileRestoreOperator instance
	fileRestoreOperator := &restorev1alpha1.FileRestoreOperator{}
	err := r.Get(ctx, req.NamespacedName, fileRestoreOperator)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("FileRestoreOperator resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get FileRestoreOperator")
		return ctrl.Result{}, err
	}

	// Update status if phase or generation changed
	if fileRestoreOperator.Status.Phase != sdkapi.PhaseDeployed ||
		fileRestoreOperator.Status.ObservedGeneration != fileRestoreOperator.Generation {
		fileRestoreOperator.Status.Phase = sdkapi.PhaseDeployed
		fileRestoreOperator.Status.ObservedGeneration = fileRestoreOperator.Generation

		if err := r.Status().Update(ctx, fileRestoreOperator); err != nil {
			log.Error(err, "Failed to update FileRestoreOperator status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FileRestoreOperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&restorev1alpha1.FileRestoreOperator{}).
		Complete(r)
}
