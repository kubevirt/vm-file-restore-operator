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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTLSProfiles_Contains(t *testing.T) {
	// Verify all TLS profile types are defined
	require.NotNil(t, TLSProfiles[TLSProfileOldType])
	require.NotNil(t, TLSProfiles[TLSProfileIntermediateType])
	require.NotNil(t, TLSProfiles[TLSProfileModernType])
}

func TestOldTLSProfile(t *testing.T) {
	profile := TLSProfiles[TLSProfileOldType]
	require.NotNil(t, profile)

	// Old profile should support TLS 1.0
	assert.Equal(t, VersionTLS10, profile.MinTLSVersion)

	// Old profile should have ciphers defined
	assert.NotEmpty(t, profile.Ciphers)
	assert.True(t, len(profile.Ciphers) > 20)
}

func TestIntermediateTLSProfile(t *testing.T) {
	profile := TLSProfiles[TLSProfileIntermediateType]
	require.NotNil(t, profile)

	// Intermediate profile should support TLS 1.2
	assert.Equal(t, VersionTLS12, profile.MinTLSVersion)

	// Intermediate profile should have ciphers defined
	assert.NotEmpty(t, profile.Ciphers)
}

func TestModernTLSProfile(t *testing.T) {
	profile := TLSProfiles[TLSProfileModernType]
	require.NotNil(t, profile)

	// Modern profile should support TLS 1.3
	assert.Equal(t, VersionTLS13, profile.MinTLSVersion)

	// Modern profile should have ciphers defined
	assert.NotEmpty(t, profile.Ciphers)
	assert.Equal(t, 3, len(profile.Ciphers))
}

func TestTLSProfileTypes(t *testing.T) {
	// Verify profile type constants
	assert.Equal(t, TLSProfileType("Old"), TLSProfileOldType)
	assert.Equal(t, TLSProfileType("Intermediate"), TLSProfileIntermediateType)
	assert.Equal(t, TLSProfileType("Modern"), TLSProfileModernType)
	assert.Equal(t, TLSProfileType("Custom"), TLSProfileCustomType)
}

func TestTLSProtocolVersions(t *testing.T) {
	// Verify protocol version constants
	assert.Equal(t, TLSProtocolVersion("VersionTLS10"), VersionTLS10)
	assert.Equal(t, TLSProtocolVersion("VersionTLS11"), VersionTLS11)
	assert.Equal(t, TLSProtocolVersion("VersionTLS12"), VersionTLS12)
	assert.Equal(t, TLSProtocolVersion("VersionTLS13"), VersionTLS13)
}

func TestTLSSecurityProfileStruct(t *testing.T) {
	// Verify we can create TLS security profile instances
	profile := &TLSSecurityProfile{
		Type: TLSProfileOldType,
		Old:  &OldTLSProfile{},
	}

	assert.NotNil(t, profile)
	assert.Equal(t, TLSProfileOldType, profile.Type)
	assert.NotNil(t, profile.Old)
}

func TestCustomTLSProfile(t *testing.T) {
	// Verify we can create a custom TLS profile
	customSpec := &CustomTLSProfile{
		TLSProfileSpec: TLSProfileSpec{
			Ciphers:       []string{"CUSTOM-CIPHER"},
			MinTLSVersion: VersionTLS12,
		},
	}

	assert.NotNil(t, customSpec)
	assert.Equal(t, "CUSTOM-CIPHER", customSpec.Ciphers[0])
	assert.Equal(t, VersionTLS12, customSpec.MinTLSVersion)
}
