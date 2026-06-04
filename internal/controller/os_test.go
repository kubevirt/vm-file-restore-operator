//nolint:goconst // Test constants are acceptable
package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "kubevirt.io/api/core/v1"
)

func TestDetectGuestOS_FromAnnotation_Windows(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"vm.kubevirt.io/os": "windows2022",
			},
		},
	}

	osType := DetectGuestOS(vmi)

	if osType != "windows" {
		t.Errorf("expected osType 'windows', got '%s'", osType)
	}
}

func TestDetectGuestOS_FromAnnotation_Linux(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"vm.kubevirt.io/os": "fedora",
			},
		},
	}

	osType := DetectGuestOS(vmi)

	if osType != "linux" {
		t.Errorf("expected osType 'linux', got '%s'", osType)
	}
}

func TestDetectGuestOS_FromGuestOSInfo_Windows(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		Status: v1.VirtualMachineInstanceStatus{
			GuestOSInfo: v1.VirtualMachineInstanceGuestOSInfo{
				Name: "Microsoft Windows Server 2022",
			},
		},
	}

	osType := DetectGuestOS(vmi)

	if osType != "windows" {
		t.Errorf("expected osType 'windows', got '%s'", osType)
	}
}

func TestDetectGuestOS_DefaultToLinux(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{}

	osType := DetectGuestOS(vmi)

	if osType != "linux" {
		t.Errorf("expected osType 'linux', got '%s'", osType)
	}
}

func TestGetMountPath_Windows(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"vm.kubevirt.io/os": "windows2022",
			},
		},
	}

	mountPath := getMountPath(vmi, "win11-pvc-snapshot-1")

	expected := `C:\backup-win11-pvc-snapshot-1`
	if mountPath != expected {
		t.Errorf("expected mountPath '%s', got '%s'", expected, mountPath)
	}
}

func TestGetMountPath_Linux(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"vm.kubevirt.io/os": "fedora",
			},
		},
	}

	mountPath := getMountPath(vmi, "my-backup-pvc")

	expected := "/backup-my-backup-pvc"
	if mountPath != expected {
		t.Errorf("expected mountPath '%s', got '%s'", expected, mountPath)
	}
}

func TestGetHelperScriptPath_Linux(t *testing.T) {
	path := GetHelperScriptPath("linux")

	expected := "/usr/local/bin/filerestore.sh"
	if path != expected {
		t.Errorf("expected path '%s', got '%s'", expected, path)
	}
}

func TestGetHelperScriptPath_Windows(t *testing.T) {
	path := GetHelperScriptPath("windows")

	expected := `"C:\Program Files\filerestore\filerestore.bat"`
	if path != expected {
		t.Errorf("expected path '%s', got '%s'", expected, path)
	}
}

func TestDetectGuestOS_AnnotationPriority(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"vm.kubevirt.io/os": "windows11",
			},
		},
		Status: v1.VirtualMachineInstanceStatus{
			GuestOSInfo: v1.VirtualMachineInstanceGuestOSInfo{
				Name: "Ubuntu 22.04",
			},
		},
	}

	osType := DetectGuestOS(vmi)

	if osType != "windows" {
		t.Errorf("expected osType 'windows' (annotation should take priority), got '%s'", osType)
	}
}

func TestDetectGuestOS_CaseSensitivity(t *testing.T) {
	testCases := []struct {
		name       string
		annotation string
	}{
		{"uppercase", "WINDOWS2022"},
		{"mixed case", "WiNdOwS10"},
		{"windows server", "Windows Server"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vmi := &v1.VirtualMachineInstance{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"vm.kubevirt.io/os": tc.annotation,
					},
				},
			}

			osType := DetectGuestOS(vmi)

			if osType != "windows" {
				t.Errorf("annotation '%s': expected osType 'windows', got '%s'", tc.annotation, osType)
			}
		})
	}
}

func TestDetectGuestOS_EmptyAnnotation(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"vm.kubevirt.io/os": "",
			},
		},
		Status: v1.VirtualMachineInstanceStatus{
			GuestOSInfo: v1.VirtualMachineInstanceGuestOSInfo{
				Name: "Microsoft Windows 10",
			},
		},
	}

	osType := DetectGuestOS(vmi)

	if osType != "windows" {
		t.Errorf("expected osType 'windows' (empty annotation should fall through to GuestOSInfo), got '%s'", osType)
	}
}

func TestGetHelperScriptPath_UnknownOS(t *testing.T) {
	path := GetHelperScriptPath("macos")

	expected := "/usr/local/bin/filerestore.sh"
	if path != expected {
		t.Errorf("expected path '%s' (should default to Linux), got '%s'", expected, path)
	}
}

func TestDetectGuestOS_GuestOSInfoCaseSensitivity(t *testing.T) {
	testCases := []struct {
		name        string
		guestOSName string
	}{
		{"uppercase", "WINDOWS"},
		{"mixed case", "WiNdOwS"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vmi := &v1.VirtualMachineInstance{
				Status: v1.VirtualMachineInstanceStatus{
					GuestOSInfo: v1.VirtualMachineInstanceGuestOSInfo{
						Name: tc.guestOSName,
					},
				},
			}

			osType := DetectGuestOS(vmi)

			if osType != "windows" {
				t.Errorf("GuestOSInfo.Name '%s': expected osType 'windows', got '%s'", tc.guestOSName, osType)
			}
		})
	}
}
