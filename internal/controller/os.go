package controller

import (
	"strings"

	v1 "kubevirt.io/api/core/v1"
)

// DetectGuestOS determines if the VMI is running Windows or Linux.
// Returns OS type ("windows" or "linux") and mount path.
func DetectGuestOS(vmi *v1.VirtualMachineInstance) (osType string, mountPath string) {
	// Strategy 1: Check vm.kubevirt.io/os annotation
	if vmi.Annotations != nil {
		if osAnnotation, exists := vmi.Annotations["vm.kubevirt.io/os"]; exists && osAnnotation != "" {
			if strings.HasPrefix(strings.ToLower(osAnnotation), "windows") {
				return "windows", "C:\\backup"
			}
			return "linux", "/backup"
		}
	}

	// Strategy 2: Fallback to GuestOSInfo.Name from guest agent
	if vmi.Status.GuestOSInfo.Name != "" {
		if strings.Contains(strings.ToLower(vmi.Status.GuestOSInfo.Name), "windows") {
			return "windows", "C:\\backup"
		}
		return "linux", "/backup"
	}

	// Strategy 3: Default to Linux
	return "linux", "/backup"
}

// GetHelperScriptPath returns the path to the helper script based on OS.
func GetHelperScriptPath(osType string) string {
	if osType == "windows" {
		return `"C:\Program Files\filerestore\filerestore.bat"`
	}
	return "/usr/local/bin/filerestore.sh"
}
