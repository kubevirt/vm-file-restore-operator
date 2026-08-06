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

	conditions "github.com/openshift/custom-resource-status/conditions/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	sdkapi "kubevirt.io/controller-lifecycle-operator-sdk/api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

const (
	reasonDeployed    = "Deployed"
	msgAvailable      = "FileRestoreOperator is available"
	msgUpgradeable    = "FileRestoreOperator is upgradeable"
	msgNotProgressing = "FileRestoreOperator is not progressing"
	msgNotDegraded    = "FileRestoreOperator is not degraded"
)

// FileRestoreOperatorReconciler reconciles a FileRestoreOperator object
type FileRestoreOperatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// OperatorVersion is the running operator version (typically from OPERATOR_VERSION env var).
	// Empty means unset; the operator will report "devel" in status.
	OperatorVersion string
}

// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=filerestoreoperators,verbs=get;list;watch
// +kubebuilder:rbac:groups=filerestore.kubevirt.io,resources=filerestoreoperators/status,verbs=get;update;patch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *FileRestoreOperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	fileRestoreOperator := &restorev1alpha1.FileRestoreOperator{}
	err := r.Get(ctx, req.NamespacedName, fileRestoreOperator)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("FileRestoreOperator resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get FileRestoreOperator")
		return ctrl.Result{}, fmt.Errorf("failed to get FileRestoreOperator %s: %w", req.NamespacedName, err)
	}

	version := r.operatorVersion()
	if !fileRestoreOperatorStatusNeedsUpdate(&fileRestoreOperator.Status, fileRestoreOperator.Generation, version) {
		return ctrl.Result{}, nil
	}

	fileRestoreOperator.Status.Phase = sdkapi.PhaseDeployed
	fileRestoreOperator.Status.ObservedGeneration = fileRestoreOperator.Generation
	fileRestoreOperator.Status.OperatorVersion = version
	fileRestoreOperator.Status.TargetVersion = version
	fileRestoreOperator.Status.ObservedVersion = version

	// Use NoHeartbeat to avoid bumping LastHeartbeatTime on every write; write
	// frequency is already controlled by fileRestoreOperatorStatusNeedsUpdate.
	conditions.SetStatusConditionNoHeartbeat(&fileRestoreOperator.Status.Conditions, conditions.Condition{
		Type:    conditions.ConditionAvailable,
		Status:  corev1.ConditionTrue,
		Reason:  reasonDeployed,
		Message: msgAvailable,
	})
	conditions.SetStatusConditionNoHeartbeat(&fileRestoreOperator.Status.Conditions, conditions.Condition{
		Type:    conditions.ConditionProgressing,
		Status:  corev1.ConditionFalse,
		Reason:  reasonDeployed,
		Message: msgNotProgressing,
	})
	conditions.SetStatusConditionNoHeartbeat(&fileRestoreOperator.Status.Conditions, conditions.Condition{
		Type:    conditions.ConditionDegraded,
		Status:  corev1.ConditionFalse,
		Reason:  reasonDeployed,
		Message: msgNotDegraded,
	})
	conditions.SetStatusConditionNoHeartbeat(&fileRestoreOperator.Status.Conditions, conditions.Condition{
		Type:    conditions.ConditionUpgradeable,
		Status:  corev1.ConditionTrue,
		Reason:  reasonDeployed,
		Message: msgUpgradeable,
	})

	if err := r.Status().Update(ctx, fileRestoreOperator); err != nil {
		logger.Error(err, "Failed to update FileRestoreOperator status")
		return ctrl.Result{}, fmt.Errorf("failed to update FileRestoreOperator %s status: %w", req.NamespacedName, err)
	}

	return ctrl.Result{}, nil
}

func (r *FileRestoreOperatorReconciler) operatorVersion() string {
	if r.OperatorVersion != "" {
		return r.OperatorVersion
	}
	return "devel"
}

func fileRestoreOperatorStatusNeedsUpdate(status *restorev1alpha1.FileRestoreOperatorStatus, generation int64, version string) bool {
	return status.Phase != sdkapi.PhaseDeployed ||
		status.ObservedGeneration != generation ||
		status.OperatorVersion != version ||
		status.TargetVersion != version ||
		status.ObservedVersion != version ||
		!isStatusConditionDeployed(status.Conditions, conditions.ConditionAvailable, corev1.ConditionTrue, msgAvailable) ||
		!isStatusConditionDeployed(status.Conditions, conditions.ConditionProgressing, corev1.ConditionFalse, msgNotProgressing) ||
		!isStatusConditionDeployed(status.Conditions, conditions.ConditionDegraded, corev1.ConditionFalse, msgNotDegraded) ||
		!isStatusConditionDeployed(status.Conditions, conditions.ConditionUpgradeable, corev1.ConditionTrue, msgUpgradeable)
}

func isStatusConditionDeployed(conds []conditions.Condition, condType conditions.ConditionType, status corev1.ConditionStatus, message string) bool {
	c := conditions.FindStatusCondition(conds, condType)
	return c != nil && c.Status == status && c.Reason == reasonDeployed && c.Message == message
}

// SetupWithManager sets up the controller with the Manager.
func (r *FileRestoreOperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&restorev1alpha1.FileRestoreOperator{}).
		Complete(r)
}
