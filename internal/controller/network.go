package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "kubevirt.io/api/core/v1"
)

// GetVMIPAddress returns the IP address to use for SSH connection.
// Tries VMI interfaces first, falls back to virt-launcher pod IP.
func GetVMIPAddress(ctx context.Context, c client.Client, vmi *v1.VirtualMachineInstance) (string, error) {
	// Issue 3: Check for nil VMI
	if vmi == nil {
		return "", fmt.Errorf("VMI is nil")
	}

	// Issue 1 & 2: Try "default" interface first (preferred)
	for _, iface := range vmi.Status.Interfaces {
		if iface.Name == "default" && iface.IP != "" {
			return iface.IP, nil
		}
	}

	// Issue 1 & 2: Fallback to first interface with any valid IP
	for _, iface := range vmi.Status.Interfaces {
		if iface.IP != "" {
			return iface.IP, nil
		}
	}

	// Fallback to virt-launcher pod IP
	pod, err := getPodForVMI(ctx, c, vmi)
	if err != nil {
		return "", err
	}

	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("pod IP not available")
	}

	return pod.Status.PodIP, nil
}

// getPodForVMI finds the virt-launcher pod for a VMI.
func getPodForVMI(ctx context.Context, c client.Client, vmi *v1.VirtualMachineInstance) (*corev1.Pod, error) {
	// List pods with virt-launcher label to reduce API load
	podList := &corev1.PodList{}
	err := c.List(ctx, podList,
		client.InNamespace(vmi.Namespace),
		client.MatchingLabels{"kubevirt.io/domain": vmi.Name},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for VMI %s/%s: %w", vmi.Namespace, vmi.Name, err)
	}

	// Find pod owned by this VMI
	for i := range podList.Items {
		for _, ownerRef := range podList.Items[i].OwnerReferences {
			if ownerRef.UID == vmi.UID {
				return &podList.Items[i], nil
			}
		}
	}

	return nil, fmt.Errorf("virt-launcher pod not found for VMI %s/%s", vmi.Namespace, vmi.Name)
}
