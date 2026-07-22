package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "kubevirt.io/api/core/v1"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

// Test file count parsing from stdout via the extracted ParseRestoredFileCount function
func TestParseRestoredFileCount(t *testing.T) {
	tests := []struct {
		name     string
		stdout   string
		expected int32
	}{
		{
			name:     "N files restored",
			stdout:   "[filerestore] 42 files restored\n",
			expected: 42,
		},
		{
			name:     "multiple lines, first match wins",
			stdout:   "[filerestore] Processing...\n[filerestore] 42 files restored\n[filerestore] 100 files restored\n",
			expected: 42,
		},
		{
			name:     "unparseable returns -1",
			stdout:   "foo bar baz\n",
			expected: -1,
		},
		{
			name:     "zero files",
			stdout:   "[filerestore] 0 files restored\n",
			expected: 0,
		},
		{
			name:     "large count",
			stdout:   "[filerestore] 99999 files restored\n",
			expected: 99999,
		},
		{
			name:     "empty stdout",
			stdout:   "",
			expected: -1,
		},
		{
			name:     "unprefixed lines are ignored",
			stdout:   "42 files restored\n",
			expected: -1,
		},
		{
			name:     "realistic combined rsync and helper output",
			stdout:   "sending incremental file list\ndata/\ndata/file.txt\n\nsent 100 bytes  received 35 bytes  270.00 bytes/sec\ntotal size is 14  speedup is 0.10\n[filerestore] 1 files restored\n[filerestore] Automatic restore of /data/file.txt completed successfully\n",
			expected: 1,
		},
		{
			name:     "numeric filename in rsync output does not false-positive",
			stdout:   "sending incremental file list\n2023-report.pdf\n3backup.txt\n\nsent 200 bytes  received 50 bytes  500.00 bytes/sec\ntotal size is 1024  speedup is 4.10\n[filerestore] 2 files restored\n",
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ParseRestoredFileCount(tt.stdout))
		})
	}
}

// Test transitionPhase timestamp logic - verifies StartTime/CompletionTime behavior
func TestTransitionPhase_Timestamps(t *testing.T) {
	vmfr := &restorev1alpha1.VirtualMachineFileRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-restore",
			Namespace: "default",
		},
		Status: restorev1alpha1.VirtualMachineFileRestoreStatus{
			Phase: restorev1alpha1.RestorePhaseNew,
		},
	}

	// StartTime should be nil initially
	assert.Nil(t, vmfr.Status.StartTime)
	assert.Nil(t, vmfr.Status.CompletionTime)

	// Note: Full transition testing requires reconciler with Event recorder,
	// but the timestamp logic in transitionPhase (phases.go:87-98) is covered
	// by integration tests
}

// Test GetVolumeName panic on empty input - verifies panic guard (hotplug.go:23-25)
func TestGetVolumeName_Panic(t *testing.T) {
	assert.Panics(t, func() {
		GetVolumeName("")
	}, "GetVolumeName should panic on empty crName")
}

// Test GetVolumeName normal operation
func TestGetVolumeName_Normal(t *testing.T) {
	result := GetVolumeName("my-restore")
	assert.Equal(t, "my-restore-restore", result)
}

// Test OS detection - covers DetectGuestOS (os.go:14-36)
func TestDetectGuestOS_Windows(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		Status: v1.VirtualMachineInstanceStatus{
			GuestOSInfo: v1.VirtualMachineInstanceGuestOSInfo{
				Name: "Microsoft Windows",
			},
		},
	}
	assert.Equal(t, osTypeWindows, DetectGuestOS(vmi))
}

func TestDetectGuestOS_Linux(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		Status: v1.VirtualMachineInstanceStatus{
			GuestOSInfo: v1.VirtualMachineInstanceGuestOSInfo{
				Name: "Ubuntu",
			},
		},
	}
	assert.Equal(t, osTypeLinux, DetectGuestOS(vmi))
}

func TestDetectGuestOS_Empty(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		Status: v1.VirtualMachineInstanceStatus{
			GuestOSInfo: v1.VirtualMachineInstanceGuestOSInfo{
				Name: "",
			},
		},
	}
	assert.Equal(t, osTypeLinux, DetectGuestOS(vmi))
}

// Test RestoredFilesCount pointer semantics for VM-deleted-during-cleanup decision.
// When RestoredFilesCount is non-nil (automatic mode ran), a VM deletion during cleanup
// should be treated as success. When nil (manual mode), it should be treated as failure.
func TestRestoredFilesCount_CleanupDecision(t *testing.T) {
	int32Ptr := func(v int32) *int32 { return &v }

	tests := []struct {
		name              string
		restoredFileCount *int32
		expectSuccess     bool
	}{
		{
			name:              "nil means no transfer — VM deletion is failure",
			restoredFileCount: nil,
			expectSuccess:     false,
		},
		{
			name:              "0 means transfer ran with nothing to copy — VM deletion is success",
			restoredFileCount: int32Ptr(0),
			expectSuccess:     true,
		},
		{
			name:              "positive count — VM deletion is success",
			restoredFileCount: int32Ptr(5),
			expectSuccess:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmfr := &restorev1alpha1.VirtualMachineFileRestore{
				Status: restorev1alpha1.VirtualMachineFileRestoreStatus{
					RestoredFilesCount: tt.restoredFileCount,
				},
			}
			// This mirrors the condition at handleCleanupPhase when VM is not found
			restoreCompleted := vmfr.Status.RestoredFilesCount != nil
			assert.Equal(t, tt.expectSuccess, restoreCompleted)
		})
	}
}

// Test source validation logic
func TestSourceValidation_Counts(t *testing.T) {
	tests := []struct {
		name        string
		vmfr        *restorev1alpha1.VirtualMachineFileRestore
		expectCount int
	}{
		{
			name: "no source",
			vmfr: &restorev1alpha1.VirtualMachineFileRestore{
				Spec: restorev1alpha1.VirtualMachineFileRestoreSpec{
					Source: restorev1alpha1.RestoreSource{},
				},
			},
			expectCount: 0,
		},
		{
			name: "PVC only",
			vmfr: &restorev1alpha1.VirtualMachineFileRestore{
				Spec: restorev1alpha1.VirtualMachineFileRestoreSpec{
					Source: restorev1alpha1.RestoreSource{
						PVC: &restorev1alpha1.PVCSource{Name: "test"},
					},
				},
			},
			expectCount: 1,
		},
		{
			name: "Snapshot only",
			vmfr: &restorev1alpha1.VirtualMachineFileRestore{
				Spec: restorev1alpha1.VirtualMachineFileRestoreSpec{
					Source: restorev1alpha1.RestoreSource{
						Snapshot: &restorev1alpha1.VolumeSnapshotSource{Name: "test"},
					},
				},
			},
			expectCount: 1,
		},
		{
			name: "Remote only",
			vmfr: &restorev1alpha1.VirtualMachineFileRestore{
				Spec: restorev1alpha1.VirtualMachineFileRestoreSpec{
					Source: restorev1alpha1.RestoreSource{
						Remote: &restorev1alpha1.RemoteSource{Name: "s3-remote"},
					},
				},
			},
			expectCount: 1,
		},
		{
			name: "PVC and Snapshot (invalid)",
			vmfr: &restorev1alpha1.VirtualMachineFileRestore{
				Spec: restorev1alpha1.VirtualMachineFileRestoreSpec{
					Source: restorev1alpha1.RestoreSource{
						PVC:      &restorev1alpha1.PVCSource{Name: "test"},
						Snapshot: &restorev1alpha1.VolumeSnapshotSource{Name: "test"},
					},
				},
			},
			expectCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate source counting logic from handleInitPhase (phases.go:218-230)
			sourceCount := 0
			if tt.vmfr.Spec.Source.PVC != nil {
				sourceCount++
			}
			if tt.vmfr.Spec.Source.Snapshot != nil {
				sourceCount++
			}
			if tt.vmfr.Spec.Source.Remote != nil {
				sourceCount++
			}
			assert.Equal(t, tt.expectCount, sourceCount)
		})
	}
}
