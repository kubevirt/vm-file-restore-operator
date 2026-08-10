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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"
)

func TestFileRestoreOperatorSpec_ImagePullPolicy(t *testing.T) {
	spec := &FileRestoreOperatorSpec{
		ImagePullPolicy: corev1.PullAlways,
	}
	assert.Equal(t, corev1.PullAlways, spec.ImagePullPolicy)
}

func TestFileRestoreOperatorSpec_TLSSecurityProfile(t *testing.T) {
	tlsProfile := &TLSSecurityProfile{
		Type: TLSProfileIntermediateType,
	}
	spec := &FileRestoreOperatorSpec{
		TLSSecurityProfile: tlsProfile,
	}
	assert.Equal(t, tlsProfile, spec.TLSSecurityProfile)
	assert.Equal(t, TLSProfileIntermediateType, spec.TLSSecurityProfile.Type)
}

func TestFileRestoreOperator_TypeMeta(t *testing.T) {
	fro := &FileRestoreOperator{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "filerestore.kubevirt.io/v1alpha1",
			Kind:       "FileRestoreOperator",
		},
	}
	assert.Equal(t, "filerestore.kubevirt.io/v1alpha1", fro.APIVersion)
	assert.Equal(t, "FileRestoreOperator", fro.Kind)
}

func TestFileRestoreOperator_ObjectMeta(t *testing.T) {
	fro := &FileRestoreOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-operator",
			Namespace: "kube-system",
		},
	}
	assert.Equal(t, "test-operator", fro.Name)
	assert.Equal(t, "kube-system", fro.Namespace)
}

func TestFileRestoreOperator_SpecAndStatus(t *testing.T) {
	spec := FileRestoreOperatorSpec{
		ImagePullPolicy: corev1.PullIfNotPresent,
	}
	status := FileRestoreOperatorStatus{
		OperatorVersion: "1.0.0",
	}
	fro := &FileRestoreOperator{
		Spec:   spec,
		Status: status,
	}
	assert.Equal(t, corev1.PullIfNotPresent, fro.Spec.ImagePullPolicy)
	assert.Equal(t, "1.0.0", fro.Status.OperatorVersion)
}

func TestFileRestoreOperatorList(t *testing.T) {
	fro1 := FileRestoreOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "operator-1",
		},
	}
	fro2 := FileRestoreOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "operator-2",
		},
	}
	list := &FileRestoreOperatorList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "filerestore.kubevirt.io/v1alpha1",
			Kind:       "FileRestoreOperatorList",
		},
		Items: []FileRestoreOperator{fro1, fro2},
	}
	assert.Equal(t, 2, len(list.Items))
	assert.Equal(t, "operator-1", list.Items[0].Name)
	assert.Equal(t, "operator-2", list.Items[1].Name)
}

func TestFileRestoreOperator_DeepCopy(t *testing.T) {
	original := &FileRestoreOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: FileRestoreOperatorSpec{
			ImagePullPolicy: corev1.PullAlways,
		},
		Status: FileRestoreOperatorStatus{
			Conditions: []metav1.Condition{
				{
					Type:   ConditionAvailable,
					Status: metav1.ConditionTrue,
					Reason: "Deployed",
				},
			},
		},
	}
	cp := original.DeepCopy()
	assert.Equal(t, original.Name, cp.Name)
	assert.Equal(t, original.Spec.ImagePullPolicy, cp.Spec.ImagePullPolicy)
	cp.Name = "modified"
	assert.NotEqual(t, original.Name, cp.Name)

	cp.Status.Conditions[0].Status = metav1.ConditionFalse
	assert.Equal(t, metav1.ConditionTrue, original.Status.Conditions[0].Status)
}

func TestFileRestoreOperatorStatus_ConditionsAndVersions(t *testing.T) {
	status := &FileRestoreOperatorStatus{
		Conditions: []metav1.Condition{
			{
				Type:   ConditionAvailable,
				Status: metav1.ConditionTrue,
				Reason: "Deployed",
			},
		},
		OperatorVersion: "0.0.1",
		TargetVersion:   "0.0.1",
		ObservedVersion: "0.0.1",
	}
	assert.Len(t, status.Conditions, 1)
	assert.Equal(t, ConditionAvailable, status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, status.Conditions[0].Status)
	assert.Equal(t, "0.0.1", status.OperatorVersion)
	assert.Equal(t, "0.0.1", status.TargetVersion)
	assert.Equal(t, "0.0.1", status.ObservedVersion)
}
