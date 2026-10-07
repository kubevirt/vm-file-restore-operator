package controller

import (
	"context"
	"testing"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v6/apis/volumesnapshot/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	v1 "kubevirt.io/api/core/v1"
	cdiv1beta1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

func TestGetVolumeName(t *testing.T) {
	name := GetVolumeName("my-restore")
	expected := "my-restore-restore"
	if name != expected {
		t.Errorf("expected %s, got %s", expected, name)
	}
}

const (
	testSnapshotNamespace = "snap-ns"
	testSnapshotName      = "snap-1"
	testContentName       = "snapcontent-1"
)

func snapshotScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, snapshotv1.AddToScheme(scheme))
	return scheme
}

func volumeSnapshot(boundContentName *string) *snapshotv1.VolumeSnapshot {
	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{Name: testSnapshotName, Namespace: testSnapshotNamespace},
	}
	if boundContentName != nil {
		vs.Status = &snapshotv1.VolumeSnapshotStatus{BoundVolumeSnapshotContentName: boundContentName}
	}
	return vs
}

func volumeSnapshotContent(mode *corev1.PersistentVolumeMode) *snapshotv1.VolumeSnapshotContent {
	return &snapshotv1.VolumeSnapshotContent{
		ObjectMeta: metav1.ObjectMeta{Name: testContentName},
		Spec:       snapshotv1.VolumeSnapshotContentSpec{SourceVolumeMode: mode},
	}
}

func TestGetSnapshotSourceVolumeMode_Block(t *testing.T) {
	block := corev1.PersistentVolumeBlock
	c := fake.NewClientBuilder().WithScheme(snapshotScheme(t)).
		WithObjects(volumeSnapshot(ptr.To(testContentName)), volumeSnapshotContent(&block)).
		Build()

	mode, err := getSnapshotSourceVolumeMode(context.Background(), c, testSnapshotName, testSnapshotNamespace)
	require.NoError(t, err)
	require.NotNil(t, mode)
	assert.Equal(t, corev1.PersistentVolumeBlock, *mode)
}

func TestGetSnapshotSourceVolumeMode_Filesystem(t *testing.T) {
	fs := corev1.PersistentVolumeFilesystem
	c := fake.NewClientBuilder().WithScheme(snapshotScheme(t)).
		WithObjects(volumeSnapshot(ptr.To(testContentName)), volumeSnapshotContent(&fs)).
		Build()

	mode, err := getSnapshotSourceVolumeMode(context.Background(), c, testSnapshotName, testSnapshotNamespace)
	require.NoError(t, err)
	require.NotNil(t, mode)
	assert.Equal(t, corev1.PersistentVolumeFilesystem, *mode)
}

// sourceVolumeMode not set on the content: default (nil), no error.
func TestGetSnapshotSourceVolumeMode_SourceVolumeModeUnset(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(snapshotScheme(t)).
		WithObjects(volumeSnapshot(ptr.To(testContentName)), volumeSnapshotContent(nil)).
		Build()

	mode, err := getSnapshotSourceVolumeMode(context.Background(), c, testSnapshotName, testSnapshotNamespace)
	require.NoError(t, err)
	assert.Nil(t, mode)
}

// Snapshot has no status yet (not bound to any content): default (nil), no error.
func TestGetSnapshotSourceVolumeMode_NoBoundContent(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(snapshotScheme(t)).
		WithObjects(volumeSnapshot(nil)).
		Build()

	mode, err := getSnapshotSourceVolumeMode(context.Background(), c, testSnapshotName, testSnapshotNamespace)
	require.NoError(t, err)
	assert.Nil(t, mode)
}

// Snapshot bound name is present but empty: default (nil), no error.
func TestGetSnapshotSourceVolumeMode_EmptyBoundContentName(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(snapshotScheme(t)).
		WithObjects(volumeSnapshot(ptr.To(""))).
		Build()

	mode, err := getSnapshotSourceVolumeMode(context.Background(), c, testSnapshotName, testSnapshotNamespace)
	require.NoError(t, err)
	assert.Nil(t, mode)
}

func TestGetSnapshotSourceVolumeMode_SnapshotNotFound(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(snapshotScheme(t)).Build()

	mode, err := getSnapshotSourceVolumeMode(context.Background(), c, testSnapshotName, testSnapshotNamespace)
	require.Error(t, err)
	assert.Nil(t, mode)
}

// Snapshot references a content that does not exist: hard error.
func TestGetSnapshotSourceVolumeMode_ContentNotFound(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(snapshotScheme(t)).
		WithObjects(volumeSnapshot(ptr.To(testContentName))).
		Build()

	mode, err := getSnapshotSourceVolumeMode(context.Background(), c, testSnapshotName, testSnapshotNamespace)
	require.Error(t, err)
	assert.Nil(t, mode)
}

const testRestoreVolumeName = "restore-1-restore" // GetVolumeName("restore-1")

func hotplugScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, snapshotv1.AddToScheme(scheme))
	require.NoError(t, cdiv1beta1.AddToScheme(scheme))
	require.NoError(t, v1.AddToScheme(scheme))
	require.NoError(t, restorev1alpha1.AddToScheme(scheme))
	return scheme
}

func snapshotRestoreCR() *restorev1alpha1.VirtualMachineFileRestore {
	return &restorev1alpha1.VirtualMachineFileRestore{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-1", Namespace: "default"},
		Spec: restorev1alpha1.VirtualMachineFileRestoreSpec{
			Target: corev1.TypedLocalObjectReference{Name: "myvm"},
			Source: restorev1alpha1.RestoreSource{
				Snapshot: &restorev1alpha1.VolumeSnapshotSource{Name: testSnapshotName, Namespace: testSnapshotNamespace},
			},
		},
	}
}

func targetVM() *v1.VirtualMachine {
	return &v1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "myvm", Namespace: "default"},
		Spec:       v1.VirtualMachineSpec{Template: &v1.VirtualMachineInstanceTemplateSpec{}},
	}
}

func getDataVolume(t *testing.T, c client.Client) *cdiv1beta1.DataVolume {
	t.Helper()
	dv := &cdiv1beta1.DataVolume{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: testRestoreVolumeName, Namespace: "default"}, dv))
	return dv
}

// HotplugVolume stamps the derived volume mode onto the DataVolume it creates.
// The DataVolume is not yet Succeeded, so hotplug returns a transient error.
func TestHotplugVolume_SnapshotDerivesVolumeMode(t *testing.T) {
	block := corev1.PersistentVolumeBlock
	c := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).
		WithStatusSubresource(&cdiv1beta1.DataVolume{}).
		WithObjects(volumeSnapshot(ptr.To(testContentName)), volumeSnapshotContent(&block)).
		Build()

	err := HotplugVolume(context.Background(), c, c, snapshotRestoreCR(), targetVM())
	require.Error(t, err)
	assert.True(t, IsTransient(err))

	dv := getDataVolume(t, c)
	require.NotNil(t, dv.Spec.Storage.VolumeMode)
	assert.Equal(t, corev1.PersistentVolumeBlock, *dv.Spec.Storage.VolumeMode)
}

// A failure to read the VolumeSnapshotContent must not fail the restore: hotplug
// falls back to the default (nil) volume mode and still creates the DataVolume.
func TestHotplugVolume_SnapshotContentReadFailsFallsBackToDefault(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).
		WithStatusSubresource(&cdiv1beta1.DataVolume{}).
		WithObjects(volumeSnapshot(ptr.To(testContentName))). // no content object -> get fails
		Build()

	err := HotplugVolume(context.Background(), c, c, snapshotRestoreCR(), targetVM())
	require.Error(t, err)
	assert.True(t, IsTransient(err), "content read failure must not hard-fail the restore")

	dv := getDataVolume(t, c)
	assert.Nil(t, dv.Spec.Storage.VolumeMode, "should fall back to default (nil) volume mode")
}

// When the DataVolume is in a terminal Failed phase, hotplug must return a non-transient error.
func TestHotplugVolume_FailedDataVolumeReturnsNonTransientError(t *testing.T) {
	existingDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{Name: testRestoreVolumeName, Namespace: "default"},
		Status:     cdiv1beta1.DataVolumeStatus{Phase: cdiv1beta1.Failed},
	}
	c := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).
		WithStatusSubresource(&cdiv1beta1.DataVolume{}).
		WithObjects(existingDV, targetVM()).
		Build()

	err := HotplugVolume(context.Background(), c, c, snapshotRestoreCR(), targetVM())
	require.Error(t, err)
	assert.False(t, IsTransient(err), "Failed DataVolume must produce a non-transient (terminal) error")
	assert.Contains(t, err.Error(), "Failed")
}

// When the DataVolume has been in a non-Succeeded phase longer than the provisioning timeout,
// hotplug must return a non-transient error so the VMFR can transition to Failed.
func TestHotplugVolume_TimedOutDataVolumeReturnsNonTransientError(t *testing.T) {
	oldTimestamp := metav1.NewTime(time.Now().Add(-11 * time.Minute))
	existingDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testRestoreVolumeName,
			Namespace:         "default",
			CreationTimestamp: oldTimestamp,
		},
		Status: cdiv1beta1.DataVolumeStatus{Phase: cdiv1beta1.Pending},
	}
	c := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).
		WithStatusSubresource(&cdiv1beta1.DataVolume{}).
		WithObjects(existingDV, targetVM()).
		Build()

	err := HotplugVolume(context.Background(), c, c, snapshotRestoreCR(), targetVM())
	require.Error(t, err)
	assert.False(t, IsTransient(err), "timed-out DataVolume must produce a non-transient error")
	assert.Contains(t, err.Error(), "timed out")
}

// When the DataVolume is in Pending but still within the provisioning timeout, hotplug
// must return a transient error so the reconciler retries.
func TestHotplugVolume_PendingDataVolumeWithinTimeoutReturnsTransientError(t *testing.T) {
	recentTimestamp := metav1.NewTime(time.Now().Add(-1 * time.Minute))
	existingDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testRestoreVolumeName,
			Namespace:         "default",
			CreationTimestamp: recentTimestamp,
		},
		Status: cdiv1beta1.DataVolumeStatus{Phase: cdiv1beta1.Pending},
	}
	c := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).
		WithStatusSubresource(&cdiv1beta1.DataVolume{}).
		WithObjects(existingDV, targetVM()).
		Build()

	err := HotplugVolume(context.Background(), c, c, snapshotRestoreCR(), targetVM())
	require.Error(t, err)
	assert.True(t, IsTransient(err), "Pending DataVolume within timeout must produce a transient error")
}

// When the DataVolume already exists, hotplug must not re-read the snapshot resources.
func TestHotplugVolume_ExistingDataVolumeSkipsSnapshotRead(t *testing.T) {
	snapshotGets := 0
	existingDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{Name: testRestoreVolumeName, Namespace: "default"},
		Status:     cdiv1beta1.DataVolumeStatus{Phase: cdiv1beta1.Succeeded},
	}
	vm := targetVM()
	c := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).
		WithStatusSubresource(&cdiv1beta1.DataVolume{}).
		WithObjects(existingDV, vm).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*snapshotv1.VolumeSnapshot); ok {
					snapshotGets++
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	err := HotplugVolume(context.Background(), c, c, snapshotRestoreCR(), vm)
	require.NoError(t, err)
	assert.Equal(t, 0, snapshotGets, "should not read the VolumeSnapshot when the DataVolume already exists")
}
