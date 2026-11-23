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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ghodss/yaml"
	csvv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"

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

func TestNetworkPolicyLabels(t *testing.T) {
	base := &ClusterServiceVersionData{
		CsvVersion:      "1.0.0",
		Namespace:       "test-namespace",
		OperatorImage:   "quay.io/kubevirt/vm-file-restore-operator:v1.0.0",
		OperatorVersion: "1.0.0",
	}

	t.Run("label absent when AddNetworkPolicyLabels is false", func(t *testing.T) {
		csv, err := NewClusterServiceVersion(base)
		require.NoError(t, err)
		podLabels := csv.Spec.InstallStrategy.StrategySpec.DeploymentSpecs[0].Spec.Template.Labels
		assert.NotContains(t, podLabels, allowAccessClusterServicesLabel)
	})

	t.Run("label present when AddNetworkPolicyLabels is true", func(t *testing.T) {
		data := *base
		data.AddNetworkPolicyLabels = true
		csv, err := NewClusterServiceVersion(&data)
		require.NoError(t, err)
		podLabels := csv.Spec.InstallStrategy.StrategySpec.DeploymentSpecs[0].Spec.Template.Labels
		assert.Equal(t, "true", podLabels[allowAccessClusterServicesLabel])
	})
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

	permissions := csv.Spec.InstallStrategy.StrategySpec.Permissions
	require.Len(t, permissions, 1)
	require.Len(t, permissions[0].Rules, 5)
	for _, expected := range []policyRuleExpectation{
		{apiGroup: "", resources: []string{"configmaps", "secrets"}, verbs: []string{"create"}},
		{
			apiGroup:      "",
			resources:     []string{"configmaps"},
			resourceNames: []string{"vm-file-restore-operator-ssh"},
			verbs:         []string{"delete", "get", "patch", "update"},
		},
		{
			apiGroup:      "",
			resources:     []string{"secrets"},
			resourceNames: []string{"vm-file-restore-operator-ssh"},
			verbs:         []string{"get"},
		},
		{apiGroup: "", resources: []string{"events"}, verbs: []string{"create", "patch"}},
		{
			apiGroup:  "coordination.k8s.io",
			resources: []string{"leases"},
			verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		},
	} {
		requirePolicyRule(t, permissions[0].Rules, expected)
	}

	clusterPerms := csv.Spec.InstallStrategy.StrategySpec.ClusterPermissions
	require.Len(t, clusterPerms, 1)
	assert.Equal(t, permissions[0].ServiceAccountName, clusterPerms[0].ServiceAccountName)
	assert.Equal(t, "vm-file-restore-operator-controller-manager", clusterPerms[0].ServiceAccountName)
	require.Len(t, clusterPerms[0].Rules, 13)
	for _, expected := range []policyRuleExpectation{
		{
			apiGroup:  "filerestore.kubevirt.io",
			resources: []string{"filerestoreoperators"},
			verbs:     []string{"get", "list", "watch"},
		},
		{
			apiGroup:  "filerestore.kubevirt.io",
			resources: []string{"filerestoreoperators/status"},
			verbs:     []string{"get", "update"},
		},
		{
			apiGroup:  "filerestore.kubevirt.io",
			resources: []string{"virtualmachinefilerestores"},
			verbs:     []string{"get", "list", "watch", "update", "patch"},
		},
		{
			apiGroup:  "filerestore.kubevirt.io",
			resources: []string{"virtualmachinefilerestores/status"},
			verbs:     []string{"get", "patch"},
		},
		{apiGroup: "kubevirt.io", resources: []string{"virtualmachines"}, verbs: []string{"get", "list", "watch", "patch"}},
		{apiGroup: "kubevirt.io", resources: []string{"virtualmachineinstances"}, verbs: []string{"get", "list", "watch"}},
		{apiGroup: "cdi.kubevirt.io", resources: []string{"datavolumes"}, verbs: []string{"create", "delete", "get"}},
		{
			apiGroup:  "snapshot.storage.k8s.io",
			resources: []string{"volumesnapshots"},
			verbs:     []string{"get", "list", "watch"},
		},
		{apiGroup: "snapshot.storage.k8s.io", resources: []string{"volumesnapshotcontents"}, verbs: []string{"get"}},
		{apiGroup: "authentication.k8s.io", resources: []string{"tokenreviews"}, verbs: []string{"create"}},
		{apiGroup: "authorization.k8s.io", resources: []string{"subjectaccessreviews"}, verbs: []string{"create"}},
		{apiGroup: "", resources: []string{"persistentvolumeclaims"}, verbs: []string{"get", "list", "watch"}},
		{apiGroup: "", resources: []string{"pods"}, verbs: []string{"list", "watch"}},
	} {
		requirePolicyRule(t, clusterPerms[0].Rules, expected)
	}
}

func TestCSVRBACPermissionsMatchManifests(t *testing.T) {
	data := &ClusterServiceVersionData{
		CsvVersion:      "1.0.0",
		Namespace:       "test-namespace",
		OperatorImage:   "quay.io/kubevirt/vm-file-restore-operator:v1.0.0",
		OperatorVersion: "1.0.0",
	}

	csv, err := NewClusterServiceVersion(data)
	require.NoError(t, err)
	require.NotNil(t, csv)

	managerRole := readClusterRole(t, filepath.Join("..", "..", "..", "config", "rbac", "role.yaml"))
	metricsAuthRole := readClusterRole(t, filepath.Join("..", "..", "..", "config", "rbac", "metrics_auth_role.yaml"))
	leaderElectionRole := readRole(t, filepath.Join("..", "..", "..", "config", "rbac", "leader_election_role.yaml"))

	want := normalizePolicyRules(append(append(managerRole.Rules, metricsAuthRole.Rules...), leaderElectionRole.Rules...))
	got := normalizePolicyRules(csvPolicyRules(csv))
	assert.Equal(t, want, got)
}

type policyRuleExpectation struct {
	apiGroup      string
	resources     []string
	resourceNames []string
	verbs         []string
}

func requirePolicyRule(t *testing.T, rules []rbacv1.PolicyRule, expected policyRuleExpectation) {
	t.Helper()
	for _, rule := range rules {
		if slices.Equal(rule.APIGroups, []string{expected.apiGroup}) &&
			slices.Equal(rule.Resources, expected.resources) &&
			slices.Equal(rule.ResourceNames, expected.resourceNames) &&
			slices.Equal(rule.Verbs, expected.verbs) {
			return
		}
	}
	require.Failf(t, "missing policy rule", "expected=%+v", expected)
}

func readClusterRole(t *testing.T, path string) *rbacv1.ClusterRole {
	t.Helper()

	contents, err := os.ReadFile(path)
	require.NoError(t, err)

	role := &rbacv1.ClusterRole{}
	require.NoError(t, yaml.Unmarshal(contents, role))
	return role
}

func readRole(t *testing.T, path string) *rbacv1.Role {
	t.Helper()

	contents, err := os.ReadFile(path)
	require.NoError(t, err)

	role := &rbacv1.Role{}
	require.NoError(t, yaml.Unmarshal(contents, role))
	return role
}

func csvPolicyRules(csv *csvv1alpha1.ClusterServiceVersion) []rbacv1.PolicyRule {
	strategy := csv.Spec.InstallStrategy.StrategySpec
	rules := make([]rbacv1.PolicyRule, 0)
	for _, permission := range strategy.Permissions {
		rules = append(rules, permission.Rules...)
	}
	for _, permission := range strategy.ClusterPermissions {
		rules = append(rules, permission.Rules...)
	}
	return rules
}

type normalizedPolicyRule struct {
	apiGroups     string
	resources     string
	resourceNames string
	verbs         string
}

func normalizePolicyRules(rules []rbacv1.PolicyRule) map[normalizedPolicyRule]struct{} {
	normalized := make(map[normalizedPolicyRule]struct{}, len(rules))
	for _, rule := range rules {
		normalized[normalizedPolicyRule{
			apiGroups:     sortedStrings(rule.APIGroups),
			resources:     sortedStrings(rule.Resources),
			resourceNames: sortedStrings(rule.ResourceNames),
			verbs:         sortedStrings(rule.Verbs),
		}] = struct{}{}
	}
	return normalized
}

func sortedStrings(values []string) string {
	values = slices.Clone(values)
	slices.Sort(values)
	return strings.Join(values, ",")
}
