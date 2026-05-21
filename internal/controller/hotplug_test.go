package controller

import (
	"testing"
)

func TestGetVolumeName(t *testing.T) {
	name := GetVolumeName("my-restore")
	expected := "my-restore-restore"
	if name != expected {
		t.Errorf("expected %s, got %s", expected, name)
	}
}
