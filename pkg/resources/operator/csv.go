/*
Copyright 2026 The KubeVirt Authors.

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

package operator

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	csvv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
)

type ClusterServiceVersionData struct {
	CsvVersion         string
	ReplacesCsvVersion string
	Namespace          string
	ImagePullPolicy    string
	IconBase64         string
	Verbosity          string
	OperatorVersion    string
	OperatorImage      string
}

func NewClusterServiceVersion(data *ClusterServiceVersionData) (*csvv1alpha1.ClusterServiceVersion, error) {
	// Validate required fields
	if data.CsvVersion == "" {
		return nil, fmt.Errorf("csv version must not be empty")
	}
	if data.Namespace == "" {
		return nil, fmt.Errorf("namespace must not be empty")
	}
	if data.OperatorImage == "" {
		return nil, fmt.Errorf("operator image must not be empty")
	}

	csv := &csvv1alpha1.ClusterServiceVersion{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterServiceVersion",
			APIVersion: "operators.coreos.com/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-file-restore-operator." + data.CsvVersion,
			Namespace: data.Namespace,
			Annotations: map[string]string{
				"capabilities": "Basic Install",
				"categories":   "Storage",
				"description":  "VM File Restore Operator provides file-level restore capabilities for KubeVirt VMs",
			},
		},
		Spec: csvv1alpha1.ClusterServiceVersionSpec{
			DisplayName: "VM File Restore Operator",
			Description: "The VM File Restore Operator enables file-level restore from VM snapshots without full VM restoration",
			Maturity:    "alpha",
			Maintainers: []csvv1alpha1.Maintainer{
				{
					Name:  "KubeVirt project",
					Email: "kubevirt-dev@googlegroups.com",
				},
			},
			Provider: csvv1alpha1.AppLink{
				Name: "KubeVirt",
			},
			Links: []csvv1alpha1.AppLink{
				{
					Name: "Source Code",
					URL:  "https://github.com/kubevirt/vm-file-restore-operator",
				},
			},
			Keywords: []string{"KubeVirt", "Virtualization", "Restore", "Backup"},
			Icon: []csvv1alpha1.Icon{
				{
					Data:      data.IconBase64,
					MediaType: "image/png",
				},
			},
			InstallModes: []csvv1alpha1.InstallMode{
				{
					Type:      csvv1alpha1.InstallModeTypeOwnNamespace,
					Supported: true,
				},
				{
					Type:      csvv1alpha1.InstallModeTypeSingleNamespace,
					Supported: true,
				},
				{
					Type:      csvv1alpha1.InstallModeTypeMultiNamespace,
					Supported: false,
				},
				{
					Type:      csvv1alpha1.InstallModeTypeAllNamespaces,
					Supported: false,
				},
			},
			InstallStrategy: csvv1alpha1.NamedInstallStrategy{
				StrategyName: "deployment",
				StrategySpec: csvv1alpha1.StrategyDetailsDeployment{
					DeploymentSpecs: []csvv1alpha1.StrategyDeploymentSpec{
						{
							Name: "vm-file-restore-operator-controller-manager",
							Spec: appsv1.DeploymentSpec{
								Replicas: int32Ptr(1),
								Selector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"control-plane": "controller-manager",
									},
								},
								Template: corev1.PodTemplateSpec{
									ObjectMeta: metav1.ObjectMeta{
										Labels: map[string]string{
											"control-plane": "controller-manager",
										},
									},
									Spec: corev1.PodSpec{
										ServiceAccountName: "vm-file-restore-operator-controller-manager",
										Containers: []corev1.Container{
											{
												Name:            "manager",
												Image:           data.OperatorImage,
												ImagePullPolicy: corev1.PullPolicy(data.ImagePullPolicy),
												Command:         []string{"/manager"},
												Args: []string{
													"--leader-elect",
													"--health-probe-bind-address=:8081",
												},
												Env: []corev1.EnvVar{
													{
														Name:  "OPERATOR_VERSION",
														Value: data.OperatorVersion,
													},
													{
														Name:  "VERBOSITY",
														Value: data.Verbosity,
													},
												},
												LivenessProbe: &corev1.Probe{
													ProbeHandler: corev1.ProbeHandler{
														HTTPGet: &corev1.HTTPGetAction{
															Path: "/healthz",
															Port: intstrint(8081),
														},
													},
													InitialDelaySeconds: 15,
													PeriodSeconds:       20,
												},
												ReadinessProbe: &corev1.Probe{
													ProbeHandler: corev1.ProbeHandler{
														HTTPGet: &corev1.HTTPGetAction{
															Path: "/readyz",
															Port: intstrint(8081),
														},
													},
													InitialDelaySeconds: 5,
													PeriodSeconds:       10,
												},
											},
										},
									},
								},
							},
						},
					},
					Permissions: []csvv1alpha1.StrategyDeploymentPermissions{
						{
							ServiceAccountName: "vm-file-restore-operator-controller-manager",
							Rules: []rbacv1.PolicyRule{
								{
									APIGroups: []string{""},
									Resources: []string{"configmaps"},
									Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
								},
								{
									APIGroups: []string{""},
									Resources: []string{"secrets"},
									Verbs:     []string{"create", "get", "list", "watch"},
								},
								{
									APIGroups: []string{"coordination.k8s.io"},
									Resources: []string{"leases"},
									Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
								},
								{
									APIGroups: []string{""},
									Resources: []string{"events"},
									Verbs:     []string{"create", "patch"},
								},
							},
						},
					},
					ClusterPermissions: []csvv1alpha1.StrategyDeploymentPermissions{
						{
							ServiceAccountName: "vm-file-restore-operator-controller-manager",
							Rules: []rbacv1.PolicyRule{
								{
									APIGroups: []string{"filerestore.kubevirt.io"},
									Resources: []string{"filerestoreoperators"},
									Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
								},
								{
									APIGroups: []string{"filerestore.kubevirt.io"},
									Resources: []string{"filerestoreoperators/status"},
									Verbs:     []string{"get", "update", "patch"},
								},
								{
									APIGroups: []string{"filerestore.kubevirt.io"},
									Resources: []string{"filerestoreoperators/finalizers"},
									Verbs:     []string{"update"},
								},
								{
									APIGroups: []string{"filerestore.kubevirt.io"},
									Resources: []string{"virtualmachinefilerestores"},
									Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
								},
								{
									APIGroups: []string{"filerestore.kubevirt.io"},
									Resources: []string{"virtualmachinefilerestores/status"},
									Verbs:     []string{"get", "update", "patch"},
								},
								{
									APIGroups: []string{"filerestore.kubevirt.io"},
									Resources: []string{"virtualmachinefilerestores/finalizers"},
									Verbs:     []string{"update"},
								},
								{
									APIGroups: []string{"kubevirt.io"},
									Resources: []string{"virtualmachines"},
									Verbs:     []string{"get", "list", "watch", "patch", "update"},
								},
								{
									APIGroups: []string{"kubevirt.io"},
									Resources: []string{"virtualmachineinstances"},
									Verbs:     []string{"get", "list", "watch"},
								},
								{
									APIGroups: []string{"cdi.kubevirt.io"},
									Resources: []string{"datavolumes"},
									Verbs:     []string{"create", "delete", "get", "list"},
								},
								{
									APIGroups: []string{"snapshot.storage.k8s.io"},
									Resources: []string{"volumesnapshots"},
									Verbs:     []string{"get", "list", "watch"},
								},
								{
									APIGroups: []string{""},
									Resources: []string{"persistentvolumeclaims"},
									Verbs:     []string{"get", "list", "watch", "create", "delete"},
								},
								{
									APIGroups: []string{""},
									Resources: []string{"pods"},
									Verbs:     []string{"get", "list", "watch", "create", "delete"},
								},
								{
									APIGroups: []string{""},
									Resources: []string{"pods/exec"},
									Verbs:     []string{"create"},
								},
							},
						},
					},
				},
			},
			CustomResourceDefinitions: csvv1alpha1.CustomResourceDefinitions{
				Owned: []csvv1alpha1.CRDDescription{
					{
						Name:        "filerestoreoperators.filerestore.kubevirt.io",
						Version:     "v1alpha1",
						Kind:        "FileRestoreOperator",
						DisplayName: "File Restore Operator",
						Description: "Represents the deployment of the VM File Restore Operator",
					},
					{
						Name:        "virtualmachinefilerestores.filerestore.kubevirt.io",
						Version:     "v1alpha1",
						Kind:        "VirtualMachineFileRestore",
						DisplayName: "Virtual Machine File Restore",
						Description: "Represents a file-level restore operation from a VM snapshot",
					},
				},
			},
		},
	}

	if data.ReplacesCsvVersion != "" {
		csv.Spec.Replaces = "vm-file-restore-operator." + data.ReplacesCsvVersion
	}

	return csv, nil
}

func int32Ptr(i int32) *int32 {
	return &i
}

func intstrint(i int) intstr.IntOrString {
	return intstr.FromInt(i)
}
