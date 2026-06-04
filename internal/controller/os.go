package controller

import (
	"strings"

	v1 "kubevirt.io/api/core/v1"
)

const (
	// OS types
	osTypeLinux   = "linux"
	osTypeWindows = "windows"

	// OS detection annotation key
	osAnnotationKey = "vm.kubevirt.io/os"

	// Default mount paths (base path, actual mount path includes volume name suffix)
	linuxMountPath   = "/backup"
	windowsMountPath = `C:\backup`

	// Helper script paths
	linuxHelperScript   = "/usr/local/bin/filerestore.sh"
	windowsHelperScript = `"C:\Program Files\filerestore\filerestore.bat"`
)

// DetectGuestOS determines if the VMI is running Windows or Linux.
// Returns OS type ("windows" or "linux").
func DetectGuestOS(vmi *v1.VirtualMachineInstance) string {
	// Strategy 1: Check vm.kubevirt.io/os annotation
	if vmi.Annotations != nil {
		if osAnnotation, exists := vmi.Annotations[osAnnotationKey]; exists && osAnnotation != "" {
			if strings.HasPrefix(strings.ToLower(osAnnotation), osTypeWindows) {
				return osTypeWindows
			}
			return osTypeLinux
		}
	}

	// Strategy 2: Fallback to GuestOSInfo.Name from guest agent
	if vmi.Status.GuestOSInfo.Name != "" {
		if strings.Contains(strings.ToLower(vmi.Status.GuestOSInfo.Name), osTypeWindows) {
			return osTypeWindows
		}
		return osTypeLinux
	}

	// Strategy 3: Default to Linux
	return osTypeLinux
}

// getMountPath returns the guest-side mount path for the restore volume based on the OS.
// The path includes the source name (PVC or snapshot) to ensure uniqueness when multiple restores exist.
func getMountPath(vmi *v1.VirtualMachineInstance, sourceName string) string {
	osType := DetectGuestOS(vmi)
	if osType == osTypeWindows {
		return windowsMountPath + "-" + sourceName
	}
	return linuxMountPath + "-" + sourceName
}

// GetHelperScriptPath returns the path to the helper script based on OS.
func GetHelperScriptPath(osType string) string {
	if osType == osTypeWindows {
		return windowsHelperScript
	}
	return linuxHelperScript
}
