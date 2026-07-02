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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClusterServiceVersion(t *testing.T) {
	tests := []struct {
		name    string
		data    *ClusterServiceVersionData
		wantErr bool
		errMsg  string
	}{
		{
			name: "Valid data with replaces version",
			data: &ClusterServiceVersionData{
				CsvVersion:         "1.0.0",
				ReplacesCsvVersion: "0.9.0",
				Namespace:          "test-namespace",
				OperatorImage:      "quay.io/kubevirt/vm-file-restore-operator:v1.0.0",
				OperatorVersion:    "1.0.0",
			},
			wantErr: false,
		},
		{
			name: "Valid data without replaces version",
			data: &ClusterServiceVersionData{
				CsvVersion:      "1.0.0",
				Namespace:       "test-namespace",
				OperatorImage:   "quay.io/kubevirt/vm-file-restore-operator:v1.0.0",
				OperatorVersion: "1.0.0",
			},
			wantErr: false,
		},
		{
			name: "Empty CSV version",
			data: &ClusterServiceVersionData{
				CsvVersion:      "",
				Namespace:       "test-namespace",
				OperatorImage:   "quay.io/kubevirt/vm-file-restore-operator:v1.0.0",
				OperatorVersion: "1.0.0",
			},
			wantErr: true,
			errMsg:  "csv version must not be empty",
		},
		{
			name: "Empty namespace",
			data: &ClusterServiceVersionData{
				CsvVersion:      "1.0.0",
				Namespace:       "",
				OperatorImage:   "quay.io/kubevirt/vm-file-restore-operator:v1.0.0",
				OperatorVersion: "1.0.0",
			},
			wantErr: true,
			errMsg:  "namespace must not be empty",
		},
		{
			name: "Empty operator image",
			data: &ClusterServiceVersionData{
				CsvVersion:      "1.0.0",
				Namespace:       "test-namespace",
				OperatorImage:   "",
				OperatorVersion: "1.0.0",
			},
			wantErr: true,
			errMsg:  "operator image must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csv, err := NewClusterServiceVersion(tt.data)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, csv)
			} else {
				require.NoError(t, err)
				require.NotNil(t, csv)

				// Verify CSV name construction
				expectedName := "vm-file-restore-operator." + tt.data.CsvVersion
				assert.Equal(t, expectedName, csv.Name)

				// Verify namespace
				assert.Equal(t, tt.data.Namespace, csv.Namespace)

				// Verify replaces field
				if tt.data.ReplacesCsvVersion != "" {
					expectedReplaces := "vm-file-restore-operator." + tt.data.ReplacesCsvVersion
					assert.Equal(t, expectedReplaces, csv.Spec.Replaces)
				} else {
					assert.Empty(t, csv.Spec.Replaces)
				}

				// Verify install strategy exists
				assert.NotNil(t, csv.Spec.InstallStrategy)

				// Verify deployment spec exists
				assert.Len(t, csv.Spec.InstallStrategy.StrategySpec.DeploymentSpecs, 1)

				// Verify RBAC permissions exist
				assert.NotEmpty(t, csv.Spec.InstallStrategy.StrategySpec.Permissions)
				assert.NotEmpty(t, csv.Spec.InstallStrategy.StrategySpec.ClusterPermissions)

				// Verify owned CRDs
				assert.Len(t, csv.Spec.CustomResourceDefinitions.Owned, 2)
				crdNames := make([]string, 2)
				for i, crd := range csv.Spec.CustomResourceDefinitions.Owned {
					crdNames[i] = crd.Name
				}
				assert.Contains(t, crdNames, "filerestoreoperators.filerestore.kubevirt.io")
				assert.Contains(t, crdNames, "virtualmachinefilerestores.filerestore.kubevirt.io")
			}
		})
	}
}

func TestCSVRBACPermissions(t *testing.T) {
	data := &ClusterServiceVersionData{
		CsvVersion:      "1.0.0",
		Namespace:       "test-namespace",
		OperatorImage:   "quay.io/kubevirt/vm-file-restore-operator:v1.0.0",
		OperatorVersion: "1.0.0",
	}

	csv, err := NewClusterServiceVersion(data)
	require.NoError(t, err)
	require.NotNil(t, csv)

	// Verify namespaced permissions include secrets
	permissions := csv.Spec.InstallStrategy.StrategySpec.Permissions
	require.Len(t, permissions, 1)

	foundSecrets := false
	for _, rule := range permissions[0].Rules {
		for _, resource := range rule.Resources {
			if resource == "secrets" {
				foundSecrets = true
				assert.Contains(t, rule.Verbs, "create")
				assert.Contains(t, rule.Verbs, "get")
			}
		}
	}
	assert.True(t, foundSecrets, "Secrets permission should be included")

	// Verify cluster permissions include datavolumes and virtualmachineinstances
	clusterPerms := csv.Spec.InstallStrategy.StrategySpec.ClusterPermissions
	require.Len(t, clusterPerms, 1)

	foundDataVolumes := false
	foundVMInstances := false
	foundVMsWithPatch := false

	for _, rule := range clusterPerms[0].Rules {
		for _, apiGroup := range rule.APIGroups {
			if apiGroup == "cdi.kubevirt.io" {
				for _, resource := range rule.Resources {
					if resource == "datavolumes" {
						foundDataVolumes = true
					}
				}
			}
			if apiGroup == "kubevirt.io" {
				for _, resource := range rule.Resources {
					if resource == "virtualmachineinstances" {
						foundVMInstances = true
					}
					if resource == "virtualmachines" {
						if assert.Contains(t, rule.Verbs, "patch") && assert.Contains(t, rule.Verbs, "update") {
							foundVMsWithPatch = true
						}
					}
				}
			}
		}
	}

	assert.True(t, foundDataVolumes, "DataVolumes permission should be included")
	assert.True(t, foundVMInstances, "VirtualMachineInstances permission should be included")
	assert.True(t, foundVMsWithPatch, "VirtualMachines should have patch/update verbs")
}
