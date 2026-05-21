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

	// Default mount paths
	linuxMountPath   = "/backup"
	windowsMountPath = `C:\backup`

	// Helper script paths
	linuxHelperScript   = "/usr/local/bin/filerestore.sh"
	windowsHelperScript = `"C:\Program Files\filerestore\filerestore.bat"`
)

// DetectGuestOS determines if the VMI is running Windows or Linux.
// Returns OS type ("windows" or "linux") and mount path.
func DetectGuestOS(vmi *v1.VirtualMachineInstance) (osType string, mountPath string) {
	// Strategy 1: Check vm.kubevirt.io/os annotation
	if vmi.Annotations != nil {
		if osAnnotation, exists := vmi.Annotations[osAnnotationKey]; exists && osAnnotation != "" {
			if strings.HasPrefix(strings.ToLower(osAnnotation), osTypeWindows) {
				return osTypeWindows, windowsMountPath
			}
			return osTypeLinux, linuxMountPath
		}
	}

	// Strategy 2: Fallback to GuestOSInfo.Name from guest agent
	if vmi.Status.GuestOSInfo.Name != "" {
		if strings.Contains(strings.ToLower(vmi.Status.GuestOSInfo.Name), osTypeWindows) {
			return osTypeWindows, windowsMountPath
		}
		return osTypeLinux, linuxMountPath
	}

	// Strategy 3: Default to Linux
	return osTypeLinux, linuxMountPath
}

// GetHelperScriptPath returns the path to the helper script based on OS.
func GetHelperScriptPath(osType string) string {
	if osType == osTypeWindows {
		return windowsHelperScript
	}
	return linuxHelperScript
}
