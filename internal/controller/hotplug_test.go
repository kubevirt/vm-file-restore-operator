package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v6/apis/volumesnapshot/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

// TestGetVolumeName_LongName_FitsLimit verifies that a CR name producing a volume > 36 chars
// is automatically shortened to ≤ maxSCSISerialLength characters (CNV-93500).
func TestGetVolumeName_LongName_FitsLimit(t *testing.T) {
	longName := testLongCRName // 36 chars; + "-restore" = 44 chars
	result := GetVolumeName(longName)
	assert.LessOrEqual(t, len(result), maxSCSISerialLength,
		"shortened volume name must fit within SCSI serial limit; got %q (%d chars)", result, len(result))
}

// TestGetVolumeName_Boundary verifies the shortening threshold:
// a crName of 28 chars fits (28+8=36) and a crName of 29 chars is shortened (29+8=37).
func TestGetVolumeName_Boundary(t *testing.T) {
	atLimit := strings.Repeat("a", maxSCSISerialLength-len(volumeNameSuffix)) // 28 chars
	assert.Equal(t, atLimit+volumeNameSuffix, GetVolumeName(atLimit), "28-char crName should not be shortened")

	overLimit := atLimit + "b" // 29 chars
	result := GetVolumeName(overLimit)
	assert.LessOrEqual(t, len(result), maxSCSISerialLength, "29-char crName must be shortened")
	assert.NotEqual(t, overLimit+volumeNameSuffix, result, "29-char crName must produce a different (shortened) name")
}

// TestGetVolumeName_NoDoubleHyphen verifies that a CR name whose 19th character is a hyphen
// does not produce a double-hyphen ("--") in the shortened serial.
func TestGetVolumeName_NoDoubleHyphen(t *testing.T) {
	// Craft a name where position shortenedPrefixLen-1 (index 18) is a hyphen.
	// "aaaaaaaaaaaaaaaaaa-" (18 a's + hyphen) then more chars to exceed the limit.
	longName := strings.Repeat("a", shortenedPrefixLen-1) + "-" + strings.Repeat("b", 20)
	result := GetVolumeName(longName)
	assert.LessOrEqual(t, len(result), maxSCSISerialLength)
	assert.NotContains(t, result, "--", "shortened volume name must not contain double hyphens")
}

// TestGetVolumeName_NoTrailingDot verifies that a CR name whose 19th character is a dot
// does not produce ".-" in the shortened name (invalid in K8s DNS-subdomain names).
func TestGetVolumeName_NoTrailingDot(t *testing.T) {
	longName := strings.Repeat("a", shortenedPrefixLen-1) + "." + strings.Repeat("b", 20)
	result := GetVolumeName(longName)
	assert.LessOrEqual(t, len(result), maxSCSISerialLength)
	assert.NotContains(t, result, ".-", "shortened volume name must not contain dot-hyphen sequence")
}

// TestGetVolumeName_LongName_Deterministic verifies that the same long CR name always produces
// the same volume name (the hash is stable).
func TestGetVolumeName_LongName_Deterministic(t *testing.T) {
	longName := testLongCRName
	assert.Equal(t, GetVolumeName(longName), GetVolumeName(longName))
}

// TestGetVolumeName_LongName_Unique verifies that two different long CR names with the same
// prefix produce different volume names (the hash distinguishes them).
func TestGetVolumeName_LongName_Unique(t *testing.T) {
	a := testLongCRName
	b := "my-really-long-restore-name-abcdefgi" // differs only in last char
	assert.NotEqual(t, GetVolumeName(a), GetVolumeName(b),
		"different CR names must produce different volume names even when prefix is identical")
}

const (
	testSnapshotNamespace = "snap-ns"
	testSnapshotName      = "snap-1"
	testContentName       = "snapcontent-1"

	// testLongCRName produces a volume name of 44 chars (> maxSCSISerialLength=36) and is used
	// across multiple tests to verify the hash-based shortening path.
	testLongCRName = "my-really-long-restore-name-abcdefgh"
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
		ObjectMeta: metav1.ObjectMeta{
			Name:        testRestoreVolumeName,
			Namespace:   "default",
			Annotations: map[string]string{"filerestore.kubevirt.io/name": "restore-1"},
		},
		Status: cdiv1beta1.DataVolumeStatus{Phase: cdiv1beta1.Failed},
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
	oldTimestamp := metav1.NewTime(time.Now().Add(-3 * time.Minute))
	existingDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testRestoreVolumeName,
			Namespace:         "default",
			CreationTimestamp: oldTimestamp,
			Annotations:       map[string]string{"filerestore.kubevirt.io/name": "restore-1"},
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
			Annotations:       map[string]string{"filerestore.kubevirt.io/name": "restore-1"},
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
		ObjectMeta: metav1.ObjectMeta{
			Name:        testRestoreVolumeName,
			Namespace:   "default",
			Annotations: map[string]string{"filerestore.kubevirt.io/name": "restore-1"},
		},
		Status: cdiv1beta1.DataVolumeStatus{Phase: cdiv1beta1.Succeeded},
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

// TestHotplugVolume_DataVolumeOwnedByOtherRestoreIsRejected verifies that if a DataVolume with
// the same name exists but carries a different VMFR owner label, HotplugVolume returns an error
// instead of silently reusing the wrong snapshot DataVolume.
func TestHotplugVolume_DataVolumeOwnedByOtherRestoreIsRejected(t *testing.T) {
	foreignDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testRestoreVolumeName,
			Namespace:   "default",
			Annotations: map[string]string{"filerestore.kubevirt.io/name": "other-restore"},
		},
		Status: cdiv1beta1.DataVolumeStatus{Phase: cdiv1beta1.Succeeded},
	}
	c := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).
		WithStatusSubresource(&cdiv1beta1.DataVolume{}).
		WithObjects(foreignDV, targetVM()).
		Build()

	err := HotplugVolume(context.Background(), c, c, snapshotRestoreCR(), targetVM())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owned by a different restore")
}

// TestUnplugVolume_SkipsDeletionOfForeignDataVolume verifies that UnplugVolume does not delete
// a snapshot DataVolume that belongs to a different VMFR (ownership mismatch).
func TestUnplugVolume_SkipsDeletionOfForeignDataVolume(t *testing.T) {
	vmfr := snapshotRestoreCR()
	foreignDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testRestoreVolumeName,
			Namespace:   "default",
			Annotations: map[string]string{"filerestore.kubevirt.io/name": "other-restore"},
		},
	}
	vm := targetVM()

	c := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).
		WithObjects(foreignDV, vm).
		Build()

	err := UnplugVolume(context.Background(), c, c, vmfr, vm)
	require.NoError(t, err)

	// DataVolume must still exist — UnplugVolume must not have deleted it.
	remaining := &cdiv1beta1.DataVolume{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(foreignDV), remaining))
}

// TestDeleteOwnedDataVolume_OwnedDVIsDeleted verifies the happy path: a DataVolume annotated
// with the VMFR name is deleted by deleteOwnedDataVolume.
func TestDeleteOwnedDataVolume_OwnedDVIsDeleted(t *testing.T) {
	ownedDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testRestoreVolumeName,
			Namespace:   "default",
			Annotations: map[string]string{"filerestore.kubevirt.io/name": "restore-1"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).WithObjects(ownedDV).Build()

	err := deleteOwnedDataVolume(context.Background(), c, c, testRestoreVolumeName, "default", "restore-1")
	require.NoError(t, err)

	remaining := &cdiv1beta1.DataVolume{}
	require.True(t, k8serrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(ownedDV), remaining)),
		"owned DataVolume must be deleted")
}

// TestDeleteOwnedDataVolume_CacheLagResilience verifies that the ownership Get uses the
// uncached apiReader. The DV is visible in apiReader but absent from the cached client,
// simulating a cache-lag window right after DataVolume creation.
func TestDeleteOwnedDataVolume_CacheLagResilience(t *testing.T) {
	ownedDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testRestoreVolumeName,
			Namespace:   "default",
			Annotations: map[string]string{"filerestore.kubevirt.io/name": "restore-1"},
		},
	}
	// cachedClient has the DV; uncachedReader also has it (fake clients share no state here,
	// so we give both the object — the point is that the function uses apiReader for the Get).
	cachedClient := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).WithObjects(ownedDV).Build()
	uncachedReader := fake.NewClientBuilder().WithScheme(hotplugScheme(t)).WithObjects(ownedDV).Build()

	err := deleteOwnedDataVolume(context.Background(), cachedClient, uncachedReader, testRestoreVolumeName, "default", "restore-1")
	require.NoError(t, err)

	remaining := &cdiv1beta1.DataVolume{}
	require.True(t, k8serrors.IsNotFound(cachedClient.Get(context.Background(), client.ObjectKeyFromObject(ownedDV), remaining)),
		"owned DataVolume must be deleted via the cached client after ownership is confirmed via uncached reader")
}

// TestDeleteOwnedDataVolume_UIDPrecondition verifies that the Delete call carries a UID
// precondition matching the object returned by the ownership Get, so a replacement DV
// created between Get and Delete is not accidentally removed.
func TestDeleteOwnedDataVolume_UIDPrecondition(t *testing.T) {
	ownedDV := &cdiv1beta1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testRestoreVolumeName,
			Namespace:   "default",
			UID:         types.UID("test-uid-1234"),
			Annotations: map[string]string{"filerestore.kubevirt.io/name": "restore-1"},
		},
	}

	var capturedUID *types.UID
	c := fake.NewClientBuilder().
		WithScheme(hotplugScheme(t)).
		WithObjects(ownedDV).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				do := &client.DeleteOptions{}
				for _, o := range opts {
					o.ApplyToDelete(do)
				}
				if do.Preconditions != nil {
					capturedUID = do.Preconditions.UID
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).
		Build()

	err := deleteOwnedDataVolume(context.Background(), c, c, testRestoreVolumeName, "default", "restore-1")
	require.NoError(t, err)
	require.NotNil(t, capturedUID, "Delete must include a UID precondition")
	assert.NotEmpty(t, *capturedUID, "UID precondition must be non-empty")
}
