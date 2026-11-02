package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

func newRetryScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, restorev1alpha1.AddToScheme(scheme))
	return scheme
}

func newRetryReconciler(scheme *runtime.Scheme, vmfr *restorev1alpha1.VirtualMachineFileRestore) *VirtualMachineFileRestoreReconciler {
	return &VirtualMachineFileRestoreReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmfr).Build(),
		Recorder: &record.FakeRecorder{},
	}
}

func TestIncrementRetryAndRequeue(t *testing.T) {
	scheme := newRetryScheme(t)
	ctx := context.Background()

	t.Run("increments attachment counter and sets timestamp", func(t *testing.T) {
		vmfr := &restorev1alpha1.VirtualMachineFileRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "r1",
				Namespace: "ns",
				Annotations: map[string]string{
					"unrelated": "preserved",
				},
			},
		}
		r := newRetryReconciler(scheme, vmfr)

		result, err := incrementRetryAndRequeue(ctx, r, vmfr, "attachment", retryBaseInterval)
		require.NoError(t, err)
		assert.True(t, result.RequeueAfter > 0)

		// Re-fetch from API to verify persistence
		fetched := &restorev1alpha1.VirtualMachineFileRestore{}
		require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(vmfr), fetched))
		assert.Equal(t, int32(1), getAnnotationInt32(fetched, annotationAttachmentRetries))
		assert.NotEmpty(t, fetched.Annotations[annotationLastAttachmentCheck])
		// Unrelated annotation must be preserved
		assert.Equal(t, "preserved", fetched.Annotations["unrelated"])
		// Status must not be touched
		assert.Empty(t, fetched.Status.Phase)
	})

	t.Run("increments SSH counter and sets timestamp", func(t *testing.T) {
		vmfr := &restorev1alpha1.VirtualMachineFileRestore{
			ObjectMeta: metav1.ObjectMeta{Name: "r2", Namespace: "ns"},
		}
		r := newRetryReconciler(scheme, vmfr)

		_, err := incrementRetryAndRequeue(ctx, r, vmfr, "ssh", retryBaseInterval)
		require.NoError(t, err)

		fetched := &restorev1alpha1.VirtualMachineFileRestore{}
		require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(vmfr), fetched))
		assert.Equal(t, int32(1), getAnnotationInt32(fetched, annotationSSHRetries))
		assert.NotEmpty(t, fetched.Annotations[annotationLastSSHCheck])
	})

	t.Run("accumulates counter across multiple calls", func(t *testing.T) {
		vmfr := &restorev1alpha1.VirtualMachineFileRestore{
			ObjectMeta: metav1.ObjectMeta{Name: "r3", Namespace: "ns"},
		}
		r := newRetryReconciler(scheme, vmfr)

		for i := 1; i <= 3; i++ {
			// Re-fetch before each call so the patch base is current
			require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(vmfr), vmfr))
			_, err := incrementRetryAndRequeue(ctx, r, vmfr, "attachment", retryBaseInterval)
			require.NoError(t, err)
		}

		fetched := &restorev1alpha1.VirtualMachineFileRestore{}
		require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(vmfr), fetched))
		assert.Equal(t, int32(3), getAnnotationInt32(fetched, annotationAttachmentRetries))
	})

	t.Run("unknown retryType returns error without patching", func(t *testing.T) {
		vmfr := &restorev1alpha1.VirtualMachineFileRestore{
			ObjectMeta: metav1.ObjectMeta{Name: "r4", Namespace: "ns"},
		}
		r := newRetryReconciler(scheme, vmfr)

		_, err := incrementRetryAndRequeue(ctx, r, vmfr, "bogus", retryBaseInterval)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown retryType")

		// Nothing should have been patched
		fetched := &restorev1alpha1.VirtualMachineFileRestore{}
		require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(vmfr), fetched))
		assert.Empty(t, fetched.Annotations)
	})
}

func TestRetryRateLimiting(t *testing.T) {
	t.Run("rate-limits when last check was recent", func(t *testing.T) {
		recentTime := time.Now().Add(-1 * time.Second).UTC().Format(time.RFC3339Nano)
		vmfr := newVMFR(map[string]string{annotationLastAttachmentCheck: recentTime})

		lastCheck := getAnnotationTime(vmfr, annotationLastAttachmentCheck)
		require.NotNil(t, lastCheck, "recent timestamp should not be treated as absent")

		elapsed := time.Since(*lastCheck)
		assert.Less(t, elapsed, retryBaseInterval, "elapsed time should be less than base interval")
	})

	t.Run("future timestamp is treated as absent (no rate limiting)", func(t *testing.T) {
		futureTime := time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339Nano)
		vmfr := newVMFR(map[string]string{annotationLastSSHCheck: futureTime})

		result := getAnnotationTime(vmfr, annotationLastSSHCheck)
		assert.Nil(t, result, "future timestamp must be treated as absent")
	})
}
