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

	networkv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testNamespace = "test-namespace"

func TestGetNetworkPolicies(t *testing.T) {
	nps := GetNetworkPolicies(testNamespace)

	require.Len(t, nps, 1, "expected exactly 1 NetworkPolicy object")
	assert.Equal(t, allowIngressToMetrics, nps[0].Name)
}

func TestNetworkPolicyMetadata(t *testing.T) {
	np := GetNetworkPolicies(testNamespace)[0]

	assert.Equal(t, "networking.k8s.io/v1", np.APIVersion)
	assert.Equal(t, "NetworkPolicy", np.Kind)
	assert.Equal(t, testNamespace, np.Namespace)
}

func TestNetworkPolicyPodSelector(t *testing.T) {
	np := GetNetworkPolicies(testNamespace)[0]

	selector := np.Spec.PodSelector.MatchLabels
	require.Contains(t, selector, managerPodLabelKey)
	assert.Equal(t, managerPodLabelVal, selector[managerPodLabelKey])
}

func TestIngressToMetricsNetworkPolicy(t *testing.T) {
	np := GetNetworkPolicies(testNamespace)[0]

	assert.Equal(t, []networkv1.PolicyType{networkv1.PolicyTypeIngress}, np.Spec.PolicyTypes)
	require.Len(t, np.Spec.Ingress, 1)
	require.Len(t, np.Spec.Ingress[0].Ports, 1)

	port := np.Spec.Ingress[0].Ports[0]
	assert.Equal(t, intstr.FromInt32(metricsPort), *port.Port)
	require.NotNil(t, port.Protocol)
	assert.Equal(t, "TCP", string(*port.Protocol))
}

func TestGetNetworkPoliciesNamespaceIsolation(t *testing.T) {
	ns1 := GetNetworkPolicies("ns-one")
	ns2 := GetNetworkPolicies("ns-two")

	require.Len(t, ns1, len(ns2))
	for i := range ns1 {
		assert.Equal(t, "ns-one", ns1[i].Namespace)
		assert.Equal(t, "ns-two", ns2[i].Namespace)
		assert.Equal(t, ns1[i].Name, ns2[i].Name)
	}
}
