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

package utils

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"

	"kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

func TestCryptoConfigFromSpec(t *testing.T) {
	tests := []struct {
		name    string
		profile *v1alpha1.TLSSecurityProfile
		wantMin uint16
		wantLen int
	}{
		{
			name:    "nil profile defaults to Intermediate",
			profile: nil,
			wantMin: tls.VersionTLS12,
			wantLen: 9, // Intermediate has 9 ciphers
		},
		{
			name: "Old profile",
			profile: &v1alpha1.TLSSecurityProfile{
				Type: v1alpha1.TLSProfileOldType,
				Old:  &v1alpha1.OldTLSProfile{},
			},
			wantMin: tls.VersionTLS10,
			wantLen: 21, // Old has 21 ciphers
		},
		{
			name: "Custom profile",
			profile: &v1alpha1.TLSSecurityProfile{
				Type: v1alpha1.TLSProfileCustomType,
				Custom: &v1alpha1.CustomTLSProfile{
					TLSProfileSpec: v1alpha1.TLSProfileSpec{
						Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
						MinTLSVersion: v1alpha1.VersionTLS13,
					},
				},
			},
			wantMin: tls.VersionTLS13,
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cryptoConfigFromSpec(tt.profile)
			assert.Equal(t, tt.wantMin, got.MinVersion)
			assert.Equal(t, tt.wantLen, len(got.CipherSuites))
		})
	}
}

func TestTLSVersionToUint16(t *testing.T) {
	tests := []struct {
		version v1alpha1.TLSProtocolVersion
		want    uint16
	}{
		{v1alpha1.VersionTLS10, tls.VersionTLS10},
		{v1alpha1.VersionTLS11, tls.VersionTLS11},
		{v1alpha1.VersionTLS12, tls.VersionTLS12},
		{v1alpha1.VersionTLS13, tls.VersionTLS13},
	}

	for _, tt := range tests {
		t.Run(string(tt.version), func(t *testing.T) {
			got := tlsVersionToUint16(tt.version)
			assert.Equal(t, tt.want, got)
		})
	}
}
