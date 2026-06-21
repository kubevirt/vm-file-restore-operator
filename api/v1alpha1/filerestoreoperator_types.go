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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sdkapi "kubevirt.io/controller-lifecycle-operator-sdk/api"
)

// FileRestoreOperatorSpec defines the desired state of FileRestoreOperator
type FileRestoreOperatorSpec struct {
	// ImagePullPolicy describes a policy for if/when to pull container images
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Infra configures node placement for operator pod
	// +optional
	Infra sdkapi.NodePlacement `json:"infra,omitempty"`

	// Workloads configures resources for restore operations
	// +optional
	Workloads sdkapi.NodePlacement `json:"workloads,omitempty"`

	// TLSSecurityProfile configures TLS settings for metrics server
	// +optional
	TLSSecurityProfile *TLSSecurityProfile `json:"tlsSecurityProfile,omitempty"`
}

// FileRestoreOperatorStatus defines the observed state of FileRestoreOperator
type FileRestoreOperatorStatus struct {
	sdkapi.Status `json:",inline"`

	// ObservedGeneration reflects the generation most recently observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

// FileRestoreOperator is the Schema for the filerestoreoperators API
type FileRestoreOperator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FileRestoreOperatorSpec   `json:"spec,omitempty"`
	Status FileRestoreOperatorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FileRestoreOperatorList contains a list of FileRestoreOperator
type FileRestoreOperatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FileRestoreOperator `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FileRestoreOperator{}, &FileRestoreOperatorList{})
}
