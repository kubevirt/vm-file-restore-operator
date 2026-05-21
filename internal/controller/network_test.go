package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "kubevirt.io/api/core/v1"
)

func TestGetVMIPAddress_FromVMIInterface(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmi",
			Namespace: "default",
			UID:       types.UID("test-vmi-uid"),
		},
		Status: v1.VirtualMachineInstanceStatus{
			Interfaces: []v1.VirtualMachineInstanceNetworkInterface{
				{
					Name: "default",
					IP:   "10.244.0.5",
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmi).Build()

	ip, err := GetVMIPAddress(context.Background(), client, vmi)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ip != "10.244.0.5" {
		t.Errorf("expected IP 10.244.0.5, got %s", ip)
	}
}

func TestGetVMIPAddress_FromPodIP(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmi",
			Namespace: "default",
			UID:       types.UID("test-vmi-uid"),
		},
		Status: v1.VirtualMachineInstanceStatus{
			Interfaces: []v1.VirtualMachineInstanceNetworkInterface{},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "virt-launcher-test-vmi-abc123",
			Namespace: "default",
			Labels: map[string]string{
				"kubevirt.io/domain": "test-vmi",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "kubevirt.io/v1",
					Kind:       "VirtualMachineInstance",
					Name:       "test-vmi",
					UID:        types.UID("test-vmi-uid"),
				},
			},
		},
		Status: corev1.PodStatus{
			PodIP: "10.244.1.10",
		},
	}

	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmi, pod).Build()

	ip, err := GetVMIPAddress(context.Background(), client, vmi)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ip != "10.244.1.10" {
		t.Errorf("expected IP 10.244.1.10, got %s", ip)
	}
}

func TestGetVMIPAddress_NoIPAvailable(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmi",
			Namespace: "default",
			UID:       types.UID("test-vmi-uid"),
		},
		Status: v1.VirtualMachineInstanceStatus{
			Interfaces: []v1.VirtualMachineInstanceNetworkInterface{},
		},
	}

	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmi).Build()

	_, err := GetVMIPAddress(context.Background(), client, vmi)
	if err == nil {
		t.Fatal("expected error when no IP available, got nil")
	}
}

// Issue 3: Test nil VMI handling
func TestGetVMIPAddress_NilVMI(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err := GetVMIPAddress(context.Background(), client, nil)
	if err == nil {
		t.Fatal("expected error when VMI is nil, got nil")
	}
	if err.Error() != "VMI is nil" {
		t.Errorf("expected error 'VMI is nil', got %v", err)
	}
}

// Issue 2: Test detection from non-default interface
func TestGetVMIPAddress_FromNonDefaultInterface(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmi",
			Namespace: "default",
			UID:       types.UID("test-vmi-uid"),
		},
		Status: v1.VirtualMachineInstanceStatus{
			Interfaces: []v1.VirtualMachineInstanceNetworkInterface{
				{
					Name: "pod",
					IP:   "10.244.0.8",
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmi).Build()

	ip, err := GetVMIPAddress(context.Background(), client, vmi)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ip != "10.244.0.8" {
		t.Errorf("expected IP 10.244.0.8, got %s", ip)
	}
}

// Issue 2: Test preference for default interface when multiple exist
func TestGetVMIPAddress_PreferDefaultInterface(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmi",
			Namespace: "default",
			UID:       types.UID("test-vmi-uid"),
		},
		Status: v1.VirtualMachineInstanceStatus{
			Interfaces: []v1.VirtualMachineInstanceNetworkInterface{
				{
					Name: "pod",
					IP:   "10.244.0.5",
				},
				{
					Name: "default",
					IP:   "10.244.0.6",
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmi).Build()

	ip, err := GetVMIPAddress(context.Background(), client, vmi)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ip != "10.244.0.6" {
		t.Errorf("expected IP 10.244.0.6 from default interface, got %s", ip)
	}
}

// Issue 2: Test fallback when default interface has empty IP
func TestGetVMIPAddress_EmptyDefaultFallbackToOther(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmi",
			Namespace: "default",
			UID:       types.UID("test-vmi-uid"),
		},
		Status: v1.VirtualMachineInstanceStatus{
			Interfaces: []v1.VirtualMachineInstanceNetworkInterface{
				{
					Name: "default",
					IP:   "",
				},
				{
					Name: "pod",
					IP:   "10.244.0.7",
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmi).Build()

	ip, err := GetVMIPAddress(context.Background(), client, vmi)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ip != "10.244.0.7" {
		t.Errorf("expected IP 10.244.0.7 from pod interface, got %s", ip)
	}
}
