package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSSHCommand_LinuxAutomatic(t *testing.T) {
	command, err := BuildSSHCommand("linux", "test-restore", "/backup", "/home/user/data")
	require.NoError(t, err)
	expected := "/usr/local/bin/filerestore.sh restore --serial test-restore --mount-path /backup --source-path /home/user/data"
	assert.Equal(t, expected, command)
}

func TestBuildSSHCommand_LinuxManual(t *testing.T) {
	command, err := BuildSSHCommand("linux", "test-restore", "/backup", "")
	require.NoError(t, err)
	expected := "/usr/local/bin/filerestore.sh restore --serial test-restore --mount-path /backup"
	assert.Equal(t, expected, command)
}

func TestBuildSSHCommand_WindowsAutomatic(t *testing.T) {
	command, err := BuildSSHCommand("windows", "test-restore", "C:\\backup", "C:\\Users\\data")
	require.NoError(t, err)
	expected := `"C:\Program Files\filerestore\filerestore.bat" restore --serial test-restore --mount-path "C:\backup" --source-path "C:\Users\data"`
	assert.Equal(t, expected, command)
}

func TestBuildSSHCommand_WindowsTrailingBackslash(t *testing.T) {
	command, err := BuildSSHCommand("windows", "test-restore", "C:\\backup", "C:\\Program Files\\")
	require.NoError(t, err)
	expected := `"C:\Program Files\filerestore\filerestore.bat" restore --serial test-restore --mount-path "C:\backup" --source-path "C:\Program Files"`
	assert.Equal(t, expected, command)
}

func TestBuildSSHCommand_LinuxTrailingSlash(t *testing.T) {
	command, err := BuildSSHCommand("linux", "test-restore", "/backup", "/home/user/data/")
	require.NoError(t, err)
	expected := "/usr/local/bin/filerestore.sh restore --serial test-restore --mount-path /backup --source-path /home/user/data"
	assert.Equal(t, expected, command)
}

func TestBuildSSHCommand_WindowsManual(t *testing.T) {
	command, err := BuildSSHCommand("windows", "test-restore", "C:\\backup", "")
	require.NoError(t, err)
	expected := `"C:\Program Files\filerestore\filerestore.bat" restore --serial test-restore --mount-path "C:\backup"`
	assert.Equal(t, expected, command)
}

func TestBuildSSHCommand_RejectsRootPaths(t *testing.T) {
	tests := []struct {
		name       string
		osType     string
		mountPath  string
		sourcePath string
	}{
		{
			name:       "Linux root as mountPath",
			osType:     "linux",
			mountPath:  "/",
			sourcePath: "/home/user/data",
		},
		{
			name:       "Linux root as sourcePath",
			osType:     "linux",
			mountPath:  "/backup",
			sourcePath: "/",
		},
		{
			name:       "Windows drive root C:\\ as mountPath",
			osType:     "windows",
			mountPath:  "C:\\",
			sourcePath: "C:\\Users\\data",
		},
		{
			name:       "Windows drive root C:\\ as sourcePath",
			osType:     "windows",
			mountPath:  "C:\\backup",
			sourcePath: "C:\\",
		},
		{
			name:       "Windows drive root C:/ as mountPath",
			osType:     "windows",
			mountPath:  "C:/",
			sourcePath: "C:\\Users\\data",
		},
		{
			name:       "Windows drive root C:/ as sourcePath",
			osType:     "windows",
			mountPath:  "C:\\backup",
			sourcePath: "C:/",
		},
		{
			name:       "Windows bare drive C: as mountPath",
			osType:     "windows",
			mountPath:  "C:",
			sourcePath: "C:\\Users\\data",
		},
		{
			name:       "Windows bare drive C: as sourcePath",
			osType:     "windows",
			mountPath:  "C:\\backup",
			sourcePath: "C:",
		},
		{
			name:       "Windows lowercase drive d:\\ as sourcePath",
			osType:     "windows",
			mountPath:  "C:\\backup",
			sourcePath: "d:\\",
		},
		{
			name:       "Windows lowercase drive e:/ as mountPath",
			osType:     "windows",
			mountPath:  "e:/",
			sourcePath: "C:\\Users\\data",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildSSHCommand(tc.osType, "test-vol", tc.mountPath, tc.sourcePath)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "root or disk-level path")
		})
	}
}

func TestBuildSSHCommand_AcceptsValidPaths(t *testing.T) {
	tests := []struct {
		name       string
		osType     string
		mountPath  string
		sourcePath string
	}{
		{
			name:       "Linux normal paths",
			osType:     "linux",
			mountPath:  "/backup",
			sourcePath: "/home/user/data",
		},
		{
			name:       "Linux deeper paths",
			osType:     "linux",
			mountPath:  "/mnt/restore",
			sourcePath: "/var/lib/data",
		},
		{
			name:       "Windows normal paths",
			osType:     "windows",
			mountPath:  "C:\\backup",
			sourcePath: "C:\\Users\\data",
		},
		{
			name:       "Windows deeper paths",
			osType:     "windows",
			mountPath:  "D:\\restore\\volume",
			sourcePath: "C:\\Program Files\\app",
		},
		{
			name:       "Empty sourcePath (manual mode)",
			osType:     "linux",
			mountPath:  "/backup",
			sourcePath: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := BuildSSHCommand(tc.osType, "test-vol", tc.mountPath, tc.sourcePath)
			require.NoError(t, err)
			assert.NotEmpty(t, cmd)
		})
	}
}

func TestIsRootOrDiskPath(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/", true},
		{"C:\\", true},
		{"C:/", true},
		{"C:", true},
		{"c:\\", true},
		{"c:/", true},
		{"d:", true},
		{"D:\\", true},
		{"Z:/", true},
		{"/backup", false},
		{"/home/user", false},
		{"C:\\backup", false},
		{"C:\\Users\\data", false},
		{"D:\\restore", false},
		{"", false},
		{"relative/path", false},
		{"CC:", false},
		{"1:", false},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.expected, isRootOrDiskPath(tc.path))
		})
	}
}

func TestBuildCleanupCommand_Linux(t *testing.T) {
	command := BuildCleanupCommand("linux", "/backup")
	expected := "/usr/local/bin/filerestore.sh cleanup --mount-path /backup"
	assert.Equal(t, expected, command)
}

func TestBuildCleanupCommand_Windows(t *testing.T) {
	command := BuildCleanupCommand("windows", "C:\\backup")
	expected := `"C:\Program Files\filerestore\filerestore.bat" cleanup --mount-path "C:\backup"`
	assert.Equal(t, expected, command)
}

func TestTruncateOutput(t *testing.T) {
	t.Run("short output unchanged", func(t *testing.T) {
		output := "line1\nline2\nline3"
		result := TruncateOutput(output, 100)
		assert.Equal(t, output, result)
	})

	t.Run("long output truncated", func(t *testing.T) {
		// Create 150 lines
		lines := make([]string, 150)
		for i := 0; i < 150; i++ {
			lines[i] = "line" + string(rune('0'+i%10))
		}
		output := ""
		for i, line := range lines {
			if i > 0 {
				output += "\n"
			}
			output += line
		}

		result := TruncateOutput(output, 100)

		// Should contain only last 100 lines
		resultLines := splitLines(result)
		assert.Equal(t, 100, len(resultLines))

		// Verify it's the last 100 lines
		expectedLines := lines[50:150]
		assert.Equal(t, expectedLines, resultLines)
	})

	t.Run("exactly max lines", func(t *testing.T) {
		output := "line1\nline2\nline3"
		result := TruncateOutput(output, 3)
		assert.Equal(t, output, result)
	})
}

// Helper function to split by newlines
func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := []string{}
	current := ""
	for _, ch := range s {
		if ch == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
