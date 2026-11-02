package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

func newVMFR(annotations map[string]string) *restorev1alpha1.VirtualMachineFileRestore {
	return &restorev1alpha1.VirtualMachineFileRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-restore",
			Namespace:   "test-ns",
			Annotations: annotations,
		},
	}
}

func TestGetAnnotationInt32(t *testing.T) {
	tests := []struct {
		name     string
		ann      map[string]string
		key      string
		expected int32
	}{
		{"absent key returns 0", nil, annotationAttachmentRetries, 0},
		{"valid positive value", map[string]string{annotationAttachmentRetries: "5"}, annotationAttachmentRetries, 5},
		{"zero value", map[string]string{annotationAttachmentRetries: "0"}, annotationAttachmentRetries, 0},
		{"negative value returns 0", map[string]string{annotationAttachmentRetries: "-3"}, annotationAttachmentRetries, 0},
		{"non-numeric returns 0", map[string]string{annotationAttachmentRetries: "bad"}, annotationAttachmentRetries, 0},
		{"empty string returns 0", map[string]string{annotationAttachmentRetries: ""}, annotationAttachmentRetries, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmfr := newVMFR(tt.ann)
			assert.Equal(t, tt.expected, getAnnotationInt32(vmfr, tt.key))
		})
	}
}

func TestSetAnnotationInt32(t *testing.T) {
	vmfr := newVMFR(nil)
	setAnnotationInt32(vmfr, annotationAttachmentRetries, 7)
	assert.Equal(t, "7", vmfr.Annotations[annotationAttachmentRetries])

	// Overwrite existing value
	setAnnotationInt32(vmfr, annotationAttachmentRetries, 42)
	assert.Equal(t, "42", vmfr.Annotations[annotationAttachmentRetries])
}

func TestGetAnnotationTime(t *testing.T) {
	past := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339Nano)
	future := time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339Nano)

	tests := []struct {
		name    string
		ann     map[string]string
		key     string
		wantNil bool
	}{
		{"absent key returns nil", nil, annotationLastAttachmentCheck, true},
		{"valid past timestamp returns value", map[string]string{annotationLastAttachmentCheck: past}, annotationLastAttachmentCheck, false},
		{"future timestamp returns nil", map[string]string{annotationLastAttachmentCheck: future}, annotationLastAttachmentCheck, true},
		{"invalid format returns nil", map[string]string{annotationLastAttachmentCheck: "not-a-time"}, annotationLastAttachmentCheck, true},
		{"empty string returns nil", map[string]string{annotationLastAttachmentCheck: ""}, annotationLastAttachmentCheck, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmfr := newVMFR(tt.ann)
			result := getAnnotationTime(vmfr, tt.key)
			if tt.wantNil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
			}
		})
	}
}

func TestSetAnnotationTime(t *testing.T) {
	vmfr := newVMFR(nil)
	now := time.Now()
	setAnnotationTime(vmfr, annotationLastSSHCheck, now)

	stored, ok := vmfr.Annotations[annotationLastSSHCheck]
	assert.True(t, ok)
	parsed, err := time.Parse(time.RFC3339Nano, stored)
	assert.NoError(t, err)
	assert.WithinDuration(t, now, parsed, time.Second)
}
