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
		wantErr    string
	}{
		{
			name:       "rejects linux source root",
			osType:     "linux",
			mountPath:  "/backup",
			sourcePath: "/",
			wantErr:    `sourcePath cannot be a root or disk path: "/"`,
		},
		{
			name:       "rejects linux mount root",
			osType:     "linux",
			mountPath:  "/",
			sourcePath: "/home/user/data",
			wantErr:    `mountPath cannot be a root or disk path: "/"`,
		},
		{
			name:       "rejects windows source drive root backslash",
			osType:     "windows",
			mountPath:  `C:\backup`,
			sourcePath: `C:\`,
			wantErr:    `sourcePath cannot be a root or disk path: "C:\\"`,
		},
		{
			name:       "rejects windows source drive root slash",
			osType:     "windows",
			mountPath:  `C:\backup`,
			sourcePath: `C:/`,
			wantErr:    `sourcePath cannot be a root or disk path: "C:/"`,
		},
		{
			name:       "rejects windows source bare drive",
			osType:     "windows",
			mountPath:  `C:\backup`,
			sourcePath: `c:`,
			wantErr:    `sourcePath cannot be a root or disk path: "c:"`,
		},
		{
			name:       "rejects windows mount drive root",
			osType:     "windows",
			mountPath:  `D:\`,
			sourcePath: `D:\Users\data`,
			wantErr:    `mountPath cannot be a root or disk path: "D:\\"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command, err := BuildSSHCommand(tt.osType, "test-restore", tt.mountPath, tt.sourcePath)
			require.Error(t, err)
			assert.Empty(t, command)
			assert.EqualError(t, err, tt.wantErr)
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
