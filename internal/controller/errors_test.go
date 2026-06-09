package controller

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewTransientError(t *testing.T) {
	msg := "test error message"
	err := NewTransientError(msg)

	assert.NotNil(t, err)
	assert.Equal(t, msg, err.Error())
}

func TestTransientError_Error(t *testing.T) {
	tests := []struct {
		name string
		msg  string
	}{
		{
			name: "simple message",
			msg:  "resource not ready",
		},
		{
			name: "formatted message",
			msg:  "DataVolume is provisioning (phase: Pending)",
		},
		{
			name: "empty message",
			msg:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewTransientError(tt.msg)
			assert.Equal(t, tt.msg, err.Error())
		})
	}
}

func TestIsTransient(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "transient error",
			err:      NewTransientError("test"),
			expected: true,
		},
		{
			name:     "regular error",
			err:      errors.New("regular error"),
			expected: false,
		},
		{
			name:     "formatted error",
			err:      fmt.Errorf("wrapped: %w", errors.New("base")),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsTransient(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTransientError_Unwrap(t *testing.T) {
	err := NewTransientError("test")
	te := err.(*TransientError)

	assert.Nil(t, te.Unwrap())
}

func TestTransientError_Format(t *testing.T) {
	msg := "test error"
	err := NewTransientError(msg)

	tests := []struct {
		name     string
		format   string
		expected string
	}{
		{
			name:     "verb v",
			format:   "%v",
			expected: msg,
		},
		{
			name:     "verb +v",
			format:   "%+v",
			expected: "transient error: " + msg,
		},
		{
			name:     "verb s",
			format:   "%s",
			expected: msg,
		},
		{
			name:     "verb q",
			format:   "%q",
			expected: fmt.Sprintf("%q", msg),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fmt.Sprintf(tt.format, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTransientError_Integration(t *testing.T) {
	// Test that TransientError works in a realistic scenario
	err := NewTransientError("PVC is provisioning")

	// Should be identifiable as transient
	assert.True(t, IsTransient(err))

	// Should format correctly in logs
	logMsg := fmt.Sprintf("Retry needed: %v", err)
	assert.Contains(t, logMsg, "PVC is provisioning")

	// Should format with details for debugging
	debugMsg := fmt.Sprintf("Error details: %+v", err)
	assert.Contains(t, debugMsg, "transient error:")
	assert.Contains(t, debugMsg, "PVC is provisioning")
}
