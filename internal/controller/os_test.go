package controller

import (
	"testing"

	v1 "kubevirt.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDetectGuestOS_FromAnnotation_Windows(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"vm.kubevirt.io/os": "windows2022",
			},
		},
	}

	osType, mountPath := DetectGuestOS(vmi)

	if osType != "windows" {
		t.Errorf("expected osType 'windows', got '%s'", osType)
	}
	if mountPath != "C:\\backup" {
		t.Errorf("expected mountPath 'C:\\backup', got '%s'", mountPath)
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

	osType, mountPath := DetectGuestOS(vmi)

	if osType != "linux" {
		t.Errorf("expected osType 'linux', got '%s'", osType)
	}
	if mountPath != "/backup" {
		t.Errorf("expected mountPath '/backup', got '%s'", mountPath)
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

	osType, mountPath := DetectGuestOS(vmi)

	if osType != "windows" {
		t.Errorf("expected osType 'windows', got '%s'", osType)
	}
	if mountPath != "C:\\backup" {
		t.Errorf("expected mountPath 'C:\\backup', got '%s'", mountPath)
	}
}

func TestDetectGuestOS_DefaultToLinux(t *testing.T) {
	vmi := &v1.VirtualMachineInstance{}

	osType, mountPath := DetectGuestOS(vmi)

	if osType != "linux" {
		t.Errorf("expected osType 'linux', got '%s'", osType)
	}
	if mountPath != "/backup" {
		t.Errorf("expected mountPath '/backup', got '%s'", mountPath)
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

	osType, mountPath := DetectGuestOS(vmi)

	if osType != "windows" {
		t.Errorf("expected osType 'windows' (annotation should take priority), got '%s'", osType)
	}
	if mountPath != "C:\\backup" {
		t.Errorf("expected mountPath 'C:\\backup', got '%s'", mountPath)
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

			osType, mountPath := DetectGuestOS(vmi)

			if osType != "windows" {
				t.Errorf("annotation '%s': expected osType 'windows', got '%s'", tc.annotation, osType)
			}
			if mountPath != "C:\\backup" {
				t.Errorf("annotation '%s': expected mountPath 'C:\\backup', got '%s'", tc.annotation, mountPath)
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

	osType, mountPath := DetectGuestOS(vmi)

	if osType != "windows" {
		t.Errorf("expected osType 'windows' (empty annotation should fall through to GuestOSInfo), got '%s'", osType)
	}
	if mountPath != "C:\\backup" {
		t.Errorf("expected mountPath 'C:\\backup', got '%s'", mountPath)
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

			osType, mountPath := DetectGuestOS(vmi)

			if osType != "windows" {
				t.Errorf("GuestOSInfo.Name '%s': expected osType 'windows', got '%s'", tc.guestOSName, osType)
			}
			if mountPath != "C:\\backup" {
				t.Errorf("GuestOSInfo.Name '%s': expected mountPath 'C:\\backup', got '%s'", tc.guestOSName, mountPath)
			}
		})
	}
}
